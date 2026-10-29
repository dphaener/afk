package rex

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestCall: exit_3 is load-bearing (not_found must surface as a CallError with
// code 3 so callers can tell "no such session" from a failure); deadline pins
// that a hung rex returns the context error rather than hanging; unscripted
// pins that the Fake fails loudly, naming the method.
func TestCall(t *testing.T) {
	cases := []struct {
		name     string
		run      RunFunc
		fake     bool
		wantOut  string
		wantCode int
		wantErr  string
		wantIs   error
	}{
		{name: "zero exit", run: func(ctx context.Context, n string, a ...string) ([]byte, error) {
			return []byte(`{"ok":true}`), nil
		}, wantOut: `{"ok":true}`},
		{name: "exit 3", run: func(ctx context.Context, n string, a ...string) ([]byte, error) {
			return nil, &CallError{Code: 3, Stderr: "not_found: no such session"}
		}, wantCode: 3, wantErr: "session.find"},
		{name: "exit 1", run: func(ctx context.Context, n string, a ...string) ([]byte, error) {
			return nil, &CallError{Code: 1, Stderr: "bad request"}
		}, wantCode: 1, wantErr: "bad request"},
		{name: "deadline", run: func(ctx context.Context, n string, a ...string) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}, wantIs: context.DeadlineExceeded},
		{name: "unscripted fake", fake: true, wantErr: "session.find"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			var c Caller
			if tc.fake {
				c = &Fake{}
			} else {
				run := tc.run
				if tc.wantIs != nil {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, 20*time.Millisecond)
					defer cancel()
				}
				c = NewCaller(run, "rex")
			}
			out, err := c.Call(ctx, "session.find", map[string]any{"name": "x"})
			if tc.wantErr == "" && tc.wantIs == nil && tc.wantCode == 0 {
				if err != nil || string(out) != tc.wantOut {
					t.Fatalf("got %q, %v", out, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error, got %q", out)
			}
			if tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q lacks %q", err, tc.wantErr)
			}
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Fatalf("error %v is not %v", err, tc.wantIs)
			}
			if tc.wantCode != 0 {
				var ce *CallError
				if !errors.As(err, &ce) || ce.ExitCode() != tc.wantCode {
					t.Fatalf("want CallError code %d, got %v", tc.wantCode, err)
				}
				t.Logf("message: %s (exit %d)", ce.Error(), ce.ExitCode())
			}
		})
	}
}

// TestCallArgs pins that args reach the command as one JSON object after
// `api call <method>`, and that nil args become {}.
func TestCallArgs(t *testing.T) {
	cases := []struct {
		name string
		args any
		want string
	}{
		{"object", map[string]any{"a": 1, "b": "x"}, `{"a":1,"b":"x"}`},
		{"nil", nil, `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			run := func(ctx context.Context, n string, a ...string) ([]byte, error) {
				got = append([]string{n}, a...)
				return []byte(`{}`), nil
			}
			if _, err := NewCaller(run, "/bin/rex").Call(context.Background(), "m.x", tc.args); err != nil {
				t.Fatal(err)
			}
			want := []string{"/bin/rex", "api", "call", "m.x", tc.want}
			if strings.Join(got, "|") != strings.Join(want, "|") {
				t.Fatalf("argv %q, want %q", got, want)
			}
		})
	}
}

// TestCallFake pins that the Fake records calls in order and prefers an
// args-specific answer.
func TestCallFake(t *testing.T) {
	f := &Fake{}
	f.On("a", `"any"`, nil).OnArgs("a", map[string]int{"n": 1}, `"one"`, nil)
	ctx := context.Background()
	r1, _ := f.Call(ctx, "a", map[string]int{"n": 1})
	r2, _ := f.Call(ctx, "a", map[string]int{"n": 2})
	if string(r1) != `"one"` || string(r2) != `"any"` {
		t.Fatalf("got %s %s", r1, r2)
	}
	if c := f.Calls(); len(c) != 2 || c[0].Method != "a" || string(c[1].Args) != `{"n":2}` {
		t.Fatalf("calls %+v", c)
	}
}

// TestNewRunMissingBinary runs no rex: a name that cannot exist must come back
// as a CallError with code 0.
func TestNewRunMissingBinary(t *testing.T) {
	_, err := NewRun(time.Second)(context.Background(), "/nonexistent/afk-no-such-bin")
	var ce *CallError
	if !errors.As(err, &ce) || ce.ExitCode() != 0 {
		t.Fatalf("got %v", err)
	}
}
