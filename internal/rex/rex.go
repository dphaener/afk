// Package rex is afk's one layer for talking to a Rex server.
//
// Every later M17 caller (hosts, the dashboard, the daemon) goes through the
// Caller interface here so each can be table-tested with a Fake instead of a
// real Rex. The package depends on no other afk package on purpose: discovery
// must never reach Rex (it must not block), and the probe, session and UI
// packages import this one, so any import back would be a cycle or a hidden
// path from a read-only scan into IPC.
package rex

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Caller makes one Rex API call and returns the server's JSON answer.
type Caller interface {
	Call(ctx context.Context, method string, args any) (json.RawMessage, error)
}

// CallError is a failed call. Code is the rex command's exit code (3 is
// not_found, 1 a bad request, per T17.1), or 0 when the command never ran to
// an exit, in which case Err says why (a deadline, a missing binary).
type CallError struct {
	Method string
	Code   int
	Stderr string
	Err    error
}

// ExitCode returns the rex command's exit code, or 0 if it did not exit.
func (e *CallError) ExitCode() int { return e.Code }

// Unwrap exposes the underlying cause so errors.Is sees context errors.
func (e *CallError) Unwrap() error { return e.Err }

// Error says the method and the command's own words in plain language.
func (e *CallError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" && e.Err != nil {
		msg = e.Err.Error()
	}
	if msg == "" {
		msg = "no message"
	}
	if e.Code != 0 {
		return fmt.Sprintf("Rex call %s failed (exit %d): %s", e.Method, e.Code, msg)
	}
	return fmt.Sprintf("Rex call %s failed: %s", e.Method, msg)
}
