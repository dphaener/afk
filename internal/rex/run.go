package rex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"time"
)

// RunFunc runs a command and returns its stdout. A non-zero exit comes back
// as a *CallError (without Method; NewCaller fills it in).
type RunFunc func(ctx context.Context, name string, args ...string) (stdout []byte, err error)

const (
	// DefaultTimeout is the deadline on one call. A Rex server that stops
	// answering must not wedge a caller.
	DefaultTimeout = 10 * time.Second
	// killGrace is how long Wait lets the command's pipes drain after the
	// deadline kills it, so a grandchild holding stdout open cannot hang us.
	killGrace = 2 * time.Second
)

// NewRun returns a RunFunc that executes the command directly (no shell) with
// stdin on the null device and a deadline of timeout.
func NewRun(timeout time.Duration) RunFunc {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.WaitDelay = killGrace
		cmd.Stdin = nil // exec connects a nil Stdin to the null device
		var out, errb bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &errb
		err := cmd.Run()
		if err == nil {
			return out.Bytes(), nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return out.Bytes(), &CallError{Stderr: errb.String(), Err: ctxErr}
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return out.Bytes(), &CallError{Code: ee.ExitCode(), Stderr: errb.String(), Err: err}
		}
		return nil, &CallError{Stderr: errb.String(), Err: err}
	}
}

type runCaller struct {
	run RunFunc
	bin string
}

// NewCaller returns a Caller that runs `bin api call <method> <json>` through
// run and returns stdout. The call goes through `api call` and never the wire
// protocol because T17.1 measured that command and chose it (docs/decisions.md,
// T17.1): it is the server's supported surface and needs no framing code here.
func NewCaller(run RunFunc, bin string) Caller {
	return &runCaller{run: run, bin: bin}
}

func (c *runCaller) Call(ctx context.Context, method string, args any) (json.RawMessage, error) {
	if args == nil {
		args = struct{}{}
	}
	body, err := json.Marshal(args)
	if err != nil {
		return nil, &CallError{Method: method, Err: err}
	}
	out, err := c.run(ctx, c.bin, "api", "call", method, string(body))
	if err != nil {
		var ce *CallError
		if errors.As(err, &ce) {
			wrapped := *ce
			wrapped.Method = method
			return nil, &wrapped
		}
		return nil, &CallError{Method: method, Err: err}
	}
	return json.RawMessage(out), nil
}
