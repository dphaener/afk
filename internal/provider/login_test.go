package provider

import (
	"context"
	"errors"
	"os"
	"testing"
)

// TestClaudeLogin covers the login check. Load-bearing rows: "logged out with
// JSON and exit error" (the CLI exits 1 yet prints JSON, so parsed output must
// beat the exit status), and "JSON without the key", "not JSON with an exit
// error" and "not JSON with exit 0", which must all be errors: an unreadable
// answer is never "logged out", or a changed CLI would raise a false alarm.
//
// The logged-in fixture has the key names `claude auth status` printed on
// 2.1.296 (re-verify by running it, PLAN 6.2), with every value replaced by a
// placeholder. A logged-out run was not captured, because logging the machine
// out is not a machine's call; that fixture is hand-written.
func TestClaudeLogin(t *testing.T) {
	in, err := os.ReadFile("testdata/auth_status_logged_in.json")
	if err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile("testdata/auth_status_logged_out.json")
	if err != nil {
		t.Fatal(err)
	}
	exit1 := errors.New("exit status 1")
	tests := []struct {
		name    string
		output  []byte
		runErr  error
		want    bool
		wantErr bool
	}{
		{"logged in (exit 0)", in, nil, true, false},
		{"logged out with JSON and exit error", out, exit1, false, false},
		{"logged out with JSON and exit 0", out, nil, false, false},
		{"JSON without the key", []byte(`{"authMethod":"x"}`), nil, false, true},
		{"JSON with a non-boolean key", []byte(`{"loggedIn":"yes"}`), nil, false, true},
		{"not JSON with an exit error", []byte("boom"), exit1, false, true},
		{"not JSON with exit 0", []byte("hello"), nil, false, true},
		{"runner error with empty output", nil, exit1, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			run := func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name != "claude" || len(args) != 2 || args[0] != "auth" || args[1] != "status" {
					t.Errorf("ran %s %v, want claude auth status", name, args)
				}
				return tc.output, tc.runErr
			}
			got, err := claude{}.Login(context.Background(), LoginDeps{Run: run})
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got.LoggedIn != tc.want {
				t.Errorf("LoggedIn = %v, want %v", got.LoggedIn, tc.want)
			}
		})
	}

	t.Run("no runner", func(t *testing.T) {
		if _, err := (claude{}).Login(context.Background(), LoginDeps{}); err == nil {
			t.Error("want an error with no runner")
		}
	})
}

// TestLoginUnsupported: the two providers with no login check must say so with
// the sentinel, so callers can skip them rather than report a failure.
func TestLoginUnsupported(t *testing.T) {
	for _, p := range []Provider{codex{}, opencode{}} {
		t.Run(p.Name(), func(t *testing.T) {
			_, err := p.Login(context.Background(), LoginDeps{})
			if !errors.Is(err, ErrLoginUnsupported) {
				t.Errorf("err = %v, want ErrLoginUnsupported", err)
			}
		})
	}
}
