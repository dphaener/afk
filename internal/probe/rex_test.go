package probe

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/dphaener/afk/internal/rex"
)

// The unknown-revision row is load-bearing: the login-server proxy reports
// revision 0, and without the RevisionKnown guard it would read as too old.
// The no-server row keeps "installed" from reading as usable, and the too-old
// row pins the boundary at revision 7.
func TestRexState(t *testing.T) {
	tests := []struct {
		name    string
		f       RexFacts
		present bool
		msg     string
	}{
		{"not installed", RexFacts{}, false, "Rex is not installed — optional; afk runs its own sessions without it"},
		{"no server", RexFacts{Installed: true}, false, "Rex is installed but its server is not running — start it with `rex server start`"},
		{"too old", RexFacts{Installed: true, ServerAnswers: true, Revision: 6, RevisionKnown: true}, false, "Rex is too old — afk needs api revision 7 or later"},
		{"unknown revision", RexFacts{Installed: true, ServerAnswers: true}, true, "Rex is running, but the proxy hides its revision, so afk cannot check that it is recent enough"},
		{"ok", RexFacts{Installed: true, ServerAnswers: true, Revision: 7, RevisionKnown: true}, true, ""},
		{"ok inside rex", RexFacts{Installed: true, ServerAnswers: true, Revision: 7, RevisionKnown: true, InsideRex: true}, true, "running inside a Rex terminal"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RexState(tc.f)
			if got.Present != tc.present || got.DegradationMessage != tc.msg {
				t.Errorf("RexState = %+v, want present=%v msg=%q", got, tc.present, tc.msg)
			}
		})
	}
}

// Both-variables rows are load-bearing: a stray REX_SESSION alone must not
// read as being inside a Rex terminal. The deadline row pins that a hung
// server counts as not answering.
func TestProbeRex(t *testing.T) {
	ok7 := func(context.Context) (rex.Server, error) { return rex.Server{Revision: 7}, nil }
	found := func(string) (string, error) { return "/bin/rex", nil }
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	tests := []struct {
		name string
		d    RexDeps
		want RexFacts
	}{
		{"not installed", RexDeps{Getenv: env(nil), LookPath: func(string) (string, error) { return "", errors.New("nope") }, Check: ok7}, RexFacts{}},
		{"server error", RexDeps{Getenv: env(nil), LookPath: found, Check: func(context.Context) (rex.Server, error) { return rex.Server{}, errors.New("refused") }}, RexFacts{Installed: true}},
		{"deadline", RexDeps{Getenv: env(nil), LookPath: found, Check: func(context.Context) (rex.Server, error) {
			return rex.Server{}, fmt.Errorf("x: %w", context.DeadlineExceeded)
		}}, RexFacts{Installed: true}},
		{"too old", RexDeps{Getenv: env(nil), LookPath: found, Check: func(context.Context) (rex.Server, error) {
			return rex.Server{Revision: 5}, fmt.Errorf("old: %w", rex.ErrTooOld)
		}}, RexFacts{Installed: true, ServerAnswers: true, Revision: 5, RevisionKnown: true}},
		{"proxy revision zero", RexDeps{Getenv: env(nil), LookPath: found, Check: func(context.Context) (rex.Server, error) { return rex.Server{}, nil }}, RexFacts{Installed: true, ServerAnswers: true}},
		{"ok", RexDeps{Getenv: env(nil), LookPath: found, Check: ok7}, RexFacts{Installed: true, ServerAnswers: true, Revision: 7, RevisionKnown: true}},
		{"only session", RexDeps{Getenv: env(map[string]string{"REX_SESSION": "s"}), LookPath: found, Check: ok7}, RexFacts{Installed: true, ServerAnswers: true, Revision: 7, RevisionKnown: true}},
		{"only block", RexDeps{Getenv: env(map[string]string{"REX_BLOCK": "b"}), LookPath: found, Check: ok7}, RexFacts{Installed: true, ServerAnswers: true, Revision: 7, RevisionKnown: true}},
		{"both", RexDeps{Getenv: env(map[string]string{"REX_SESSION": "s", "REX_BLOCK": "b"}), LookPath: found, Check: ok7}, RexFacts{Installed: true, ServerAnswers: true, Revision: 7, RevisionKnown: true, InsideRex: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ProbeRex(context.Background(), tc.d); got != tc.want {
				t.Errorf("ProbeRex = %+v, want %+v", got, tc.want)
			}
		})
	}
}
