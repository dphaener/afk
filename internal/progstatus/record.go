// Package progstatus reads and writes Program Status reports, the OSC 7501
// escape sequence a program uses to tell its terminal whether it is idle,
// working, done, blocked or in error.
//
// It exists because three parts of afk need the same building block: the
// host records what an agent reports (T17.4), afk reports its own status
// (T17.5) and the host writes afk's own record between the agent's escape
// sequences without tearing them (T17.7). The host calls it on the output
// pump's hot path, so the package is pure (standard library only, no I/O,
// clock or goroutines) and never allocates without bound: every input it
// keeps is capped at the spec's 4096-byte sequence limit.
//
// It implements the Program Status Protocol, revision 0.4 (2026-10-10),
// published at https://www.superlogical.com/rex/docs/build/program-status.
// Where the spec leaves a choice open, the doc comment on the function that
// makes it says which way it went and why.
package progstatus

import (
	"strings"
	"unicode/utf8"
)

// State is the value of a report's `state` key.
type State string

// The states the spec defines. StateClear is not a state a terminal stores:
// it removes the addressed record and every record beneath it.
const (
	StateIdle    State = "idle"
	StateWorking State = "working"
	StateDone    State = "done"
	StateBlocked State = "blocked"
	StateError   State = "error"
	StateClear   State = "clear"
)

// Kind is the value of a report's `kind` key, which says what a blocked
// program is waiting for.
type Kind string

// The kinds the spec defines.
const (
	KindPermission Kind = "permission"
	KindQuestion   Kind = "question"
	KindAuth       Kind = "auth"
)

// Record is one report. Title and Msg hold the decoded text, not the base64
// that travels on the wire. An empty string means the key is absent, and
// Progress means nothing unless HasProgress is true (an absent progress is
// "busy, no percentage", which 0 would misstate).
type Record struct {
	State       State
	ID          string
	Kind        Kind
	Progress    int
	HasProgress bool
	App         string
	Title       string
	Msg         string
}

// The spec's Limits table. Every one is a hard cap: a report over any of
// them is discarded whole by a terminal, so Encode refuses to write one.
const (
	// MaxSequence is the whole sequence, OSC through ST.
	MaxSequence = 4096
	// MaxKey is the longest key, in bytes.
	MaxKey = 16
	// MaxMsgEncoded and MaxMsgDecoded cap msg on the wire and as text.
	MaxMsgEncoded = 2732
	MaxMsgDecoded = 2048
	// MaxTitleEncoded and MaxTitleDecoded cap title on the wire and as text.
	MaxTitleEncoded = 256
	MaxTitleDecoded = 192
	// MaxApp is the longest app value.
	MaxApp = 32
	// MaxID is the longest id in all, MaxSegment the longest piece between
	// slashes and MaxDepth the most pieces.
	MaxID      = 128
	MaxSegment = 32
	MaxDepth   = 8
)

func validState(s State) bool {
	switch s {
	case StateIdle, StateWorking, StateDone, StateBlocked, StateError, StateClear:
		return true
	}
	return false
}

func validKind(k Kind) bool {
	switch k {
	case KindPermission, KindQuestion, KindAuth:
		return true
	}
	return false
}

// segmentByte reports whether b is in [A-Za-z0-9_.+-], the set shared by an
// id segment and an app.
func segmentByte(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	}
	return b == '_' || b == '.' || b == '+' || b == '-'
}

// validApp reports whether s matches [A-Za-z0-9_.+-]{1,32}.
func validApp(s string) bool {
	if len(s) < 1 || len(s) > MaxApp {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !segmentByte(s[i]) {
			return false
		}
	}
	return true
}

// validID reports whether s matches the spec's id grammar and its 128 byte,
// 32 per segment and 8 deep limits. The empty string is not a valid id: the
// root record is addressed by leaving the key out, and Parse refuses
// `id=` rather than guess that it meant the root (see its doc comment).
func validID(s string) bool {
	if len(s) < 1 || len(s) > MaxID {
		return false
	}
	depth := 0
	for _, seg := range strings.Split(s, "/") {
		depth++
		if depth > MaxDepth || !validApp(seg) || len(seg) > MaxSegment {
			return false
		}
	}
	return true
}

// validText reports whether s is valid UTF-8 with no control character
// (U+0000-001F, U+007F, U+0080-009F). Invalid UTF-8 is refused because the
// spec says free text is base64 of UTF-8 but names no rule for bytes that
// are not; a terminal cannot show them and the safe reading is to discard.
func validText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r <= 0x1f || (r >= 0x7f && r <= 0x9f) {
			return false
		}
	}
	return true
}
