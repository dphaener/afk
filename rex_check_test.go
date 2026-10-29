package main

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/probe"
	"github.com/dphaener/afk/internal/rex"
)

// The exit-code column is load-bearing: 1 for not installed, no server and
// too old (each with a next step in the text), 0 for both session counts. The
// "none" row pins that an empty session list is success, not a failure.
func TestRexCheck(t *testing.T) {
	found := func(string) (string, error) { return "/bin/rex", nil }
	missing := func(string) (string, error) { return "", exec.ErrNotFound }
	status := func(rev string) func(context.Context) (rex.Server, error) {
		return func(ctx context.Context) (rex.Server, error) {
			f := (&rex.Fake{}).On("server.status", `{"api_revision":`+rev+`}`, nil)
			return rex.CheckServer(ctx, f)
		}
	}
	down := func(context.Context) (rex.Server, error) { return rex.Server{}, errors.New("no server") }

	tests := []struct {
		name     string
		d        probe.RexDeps
		list     string
		wantCode int
		want     []string
	}{
		{"not installed", probe.RexDeps{LookPath: missing, Check: down}, "", 1, []string{"Rex installed: no", "Rex is not installed"}},
		{"no server", probe.RexDeps{LookPath: found, Check: down}, "", 1, []string{"Rex server answers: no", "rex server start"}},
		{"too old", probe.RexDeps{LookPath: found, Check: status("6")}, "", 1, []string{"api revision: 6", "too old", "Update Rex"}},
		{"ok two sessions", probe.RexDeps{LookPath: found, Check: status("7")}, `{"sessions":[{"session_id":"s1","label":"one"},{"session_id":"s2","label":"two"}]}`, 0, []string{"api revision: 7", "sessions (2)", "s1  one", "s2  two"}},
		{"ok none", probe.RexDeps{LookPath: found, Check: status("7")}, `{"sessions":[]}`, 0, []string{"api revision: 7", "no sessions"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.d.Getenv = func(string) string { return "" }
			var out bytes.Buffer
			f := (&rex.Fake{}).On("session.list", tc.list, nil)
			code := runRexCheck(context.Background(), &out, tc.d, f)
			if code != tc.wantCode {
				t.Errorf("exit = %d, want %d\n%s", code, tc.wantCode, out.String())
			}
			for _, w := range tc.want {
				if !strings.Contains(out.String(), w) {
					t.Errorf("output lacks %q:\n%s", w, out.String())
				}
			}
			if tc.wantCode == 1 && len(f.Calls()) != 0 {
				t.Errorf("listed sessions despite failing: %v", f.Calls())
			}
		})
	}
}
