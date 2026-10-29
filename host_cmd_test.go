package main

import (
	"errors"
	"flag"
	"io"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/host"
	"github.com/dphaener/afk/internal/rex"
	"github.com/dphaener/afk/internal/session"
)

// noEnv is an environment with nothing in it, so a test never reads the real
// one: a developer running the suite inside a Rex terminal has REX_BLOCK set.
func noEnv(string) string { return "" }

// envOf is an environment holding exactly vars.
func envOf(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

// TestHostFlagsProvider parses real `_host` argument lists through the same
// flag set cmdHost uses. Load-bearing rows: "absent" is every Claude host,
// whose spawner never passes --provider, so it must leave Provider empty
// (host.json stays free of the key and restart resumes as claude); "codex"
// proves the flag reaches ServeConfig, which is the only way a wedged restart
// learns to resume with `codex resume`; "after --" proves a --provider token in
// the child's argv is the child's, not the host's.
func TestHostFlagsProvider(t *testing.T) {
	base := []string{"--name", "s", "--worktree", "/wt", "--socket", "/sock", "--log", "/log", "--meta", "/meta"}
	cases := []struct {
		name         string
		args         []string
		wantProvider string
		wantArgv     []string
	}{
		{
			name:         "absent",
			args:         append(append([]string{}, base...), "--", "claude", "/afk:go --session s"),
			wantProvider: "",
			wantArgv:     []string{"claude", "/afk:go --session s"},
		},
		{
			name:         "codex",
			args:         append(append([]string{}, base...), "--provider", "codex", "--", "codex"),
			wantProvider: "codex",
			wantArgv:     []string{"codex"},
		},
		{
			name:         "after -- belongs to the child",
			args:         append(append([]string{}, base...), "--", "codex", "--provider", "x"),
			wantProvider: "",
			wantArgv:     []string{"codex", "--provider", "x"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs, flags := newHostFlagSet(flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			if err := fs.Parse(tc.args); err != nil {
				t.Fatalf("Parse(%q) = %v", tc.args, err)
			}
			cfg, err := flags.serveConfig(fs.Args(), noEnv)
			if err != nil {
				t.Fatalf("serveConfig() error = %v", err)
			}
			if cfg.Provider != tc.wantProvider {
				t.Errorf("Provider = %q, want %q", cfg.Provider, tc.wantProvider)
			}
			if strings.Join(cfg.Argv, "\x00") != strings.Join(tc.wantArgv, "\x00") {
				t.Errorf("Argv = %q, want %q", cfg.Argv, tc.wantArgv)
			}
			if cfg.Name != "s" || cfg.Worktree != "/wt" || cfg.Socket != "/sock" || cfg.LogPath != "/log" || cfg.MetaPath != "/meta" {
				t.Errorf("serveConfig() = %+v, want the base flags carried through", cfg)
			}
		})
	}
}

// TestHostFlagsMissingArgvIsAnError keeps buildServeConfig's one guard in
// force on the flag path: no child argv after -- is refused, provider or not.
func TestHostFlagsMissingArgvIsAnError(t *testing.T) {
	fs, flags := newHostFlagSet(flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse([]string{"--name", "s", "--provider", "codex"}); err != nil {
		t.Fatalf("Parse = %v", err)
	}
	if cfg, err := flags.serveConfig(fs.Args(), noEnv); err == nil {
		t.Fatalf("serveConfig() = %+v, want an error for a missing child argv", cfg)
	}
}

// TestHostFlagsProbeUnattached parses real `_host` argument lists for the
// --probe-unattached switch. Load-bearing rows: "absent" is every session a
// person starts, whose host must keep the rule that a session with nobody
// attached is never probed, so the default has to be false; "present" proves
// the flag reaches ServeConfig, which is the only way a session the daemon
// supervises (it attaches no client) ever gets a wedge verdict; "after --"
// proves the same token in the child's argv is the child's and does not
// switch the host's probing on.
func TestHostFlagsProbeUnattached(t *testing.T) {
	base := []string{"--name", "s", "--worktree", "/wt", "--socket", "/sock", "--log", "/log", "--meta", "/meta"}
	cases := []struct {
		name      string
		args      []string
		wantProbe bool
		wantArgv  []string
	}{
		{
			name:      "absent",
			args:      append(append([]string{}, base...), "--", "agent", "go"),
			wantProbe: false,
			wantArgv:  []string{"agent", "go"},
		},
		{
			name:      "present",
			args:      append(append([]string{}, base...), "--probe-unattached", "--", "agent", "go"),
			wantProbe: true,
			wantArgv:  []string{"agent", "go"},
		},
		{
			name:      "explicitly false",
			args:      append(append([]string{}, base...), "--probe-unattached=false", "--", "agent"),
			wantProbe: false,
			wantArgv:  []string{"agent"},
		},
		{
			name:      "beside the provider flag",
			args:      append(append([]string{}, base...), "--provider", "codex", "--probe-unattached", "--", "agent"),
			wantProbe: true,
			wantArgv:  []string{"agent"},
		},
		{
			name:      "after -- belongs to the child",
			args:      append(append([]string{}, base...), "--", "agent", "--probe-unattached"),
			wantProbe: false,
			wantArgv:  []string{"agent", "--probe-unattached"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs, flags := newHostFlagSet(flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			if err := fs.Parse(tc.args); err != nil {
				t.Fatalf("Parse(%q) = %v", tc.args, err)
			}
			cfg, err := flags.serveConfig(fs.Args(), noEnv)
			if err != nil {
				t.Fatalf("serveConfig() error = %v", err)
			}
			if cfg.ProbeUnattached != tc.wantProbe {
				t.Errorf("ProbeUnattached = %v, want %v", cfg.ProbeUnattached, tc.wantProbe)
			}
			if strings.Join(cfg.Argv, "\x00") != strings.Join(tc.wantArgv, "\x00") {
				t.Errorf("Argv = %q, want %q", cfg.Argv, tc.wantArgv)
			}
		})
	}
}

// TestHostFlagsStdioRex parses real `_host` argument lists for --stdio and
// the Rex ids. Load-bearing rows: the three refusals, each checked by its
// message so a wrong refusal fails (a Rex id without --stdio would record a
// block for a host that owns its own terminal; a half pair could not be found
// again), and "absent", which must leave every ordinary host unchanged.
func TestHostFlagsStdioRex(t *testing.T) {
	base := []string{"--name", "s", "--worktree", "/wt", "--socket", "/sock", "--log", "/log", "--meta", "/meta"}
	cases := []struct {
		name      string
		extra     []string
		wantErr   string
		wantStdio bool
		wantRex   *host.RexBlock
	}{
		{name: "absent"},
		{name: "stdio alone", extra: []string{"--stdio"}, wantStdio: true},
		{name: "stdio with ids", extra: []string{"--stdio", "--rex-server", "srv", "--rex-session", "S1", "--rex-window", "W1", "--rex-block", "B1"},
			wantStdio: true, wantRex: &host.RexBlock{Server: "srv", SessionID: "S1", WindowID: "W1", BlockID: "B1"}},
		{name: "ids without stdio", extra: []string{"--rex-session", "S1", "--rex-block", "B1"}, wantErr: "only apply with --stdio"},
		{name: "server without stdio", extra: []string{"--rex-server", "srv"}, wantErr: "only apply with --stdio"},
		{name: "session without block", extra: []string{"--stdio", "--rex-session", "S1"}, wantErr: "--rex-session needs --rex-block"},
		{name: "block without session", extra: []string{"--stdio", "--rex-block", "B1"}, wantErr: "--rex-block needs --rex-session"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs, flags := newHostFlagSet(flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			args := append(append(append([]string{}, base...), tc.extra...), "--", "agent")
			if err := fs.Parse(args); err != nil {
				t.Fatalf("Parse = %v", err)
			}
			cfg, err := flags.serveConfig(fs.Args(), noEnv)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("serveConfig: %v", err)
			}
			if cfg.Stdio != tc.wantStdio {
				t.Errorf("Stdio = %v, want %v", cfg.Stdio, tc.wantStdio)
			}
			if (cfg.Rex == nil) != (tc.wantRex == nil) || (tc.wantRex != nil && *cfg.Rex != *tc.wantRex) {
				t.Errorf("Rex = %+v, want %+v", cfg.Rex, tc.wantRex)
			}
		})
	}
}

// TestHostCmdRexEnv parses real `_host` argument lists with an environment
// standing in for the one Rex gives a block's command. Load-bearing rows:
// "env fills the ids" is how host.json learns its block at all (the spawner
// cannot pass a block id that does not exist yet); "flags win, whole" proves
// the two sources never mix; "env without stdio" keeps a detached host that
// was started from a Rex shell free of a block it does not run in; "half the
// env" records what it found instead of refusing to start inside a block.
func TestHostCmdRexEnv(t *testing.T) {
	base := []string{"--name", "s", "--worktree", "/wt", "--socket", "/sock", "--log", "/log", "--meta", "/meta"}
	full := map[string]string{"REX_SERVER": "srv", "REX_SESSION": "session:s1", "REX_BLOCK": "block:b1"}
	cases := []struct {
		name    string
		extra   []string
		env     map[string]string
		wantRex *host.RexBlock
	}{
		{name: "env fills the ids", extra: []string{"--stdio"}, env: full,
			wantRex: &host.RexBlock{Server: "srv", SessionID: "session:s1", BlockID: "block:b1"}},
		{name: "flags win, whole", extra: []string{"--stdio", "--rex-session", "S1", "--rex-block", "B1"}, env: full,
			wantRex: &host.RexBlock{SessionID: "S1", BlockID: "B1"}},
		{name: "env without stdio", env: full},
		{name: "stdio with no env", extra: []string{"--stdio"}},
		{name: "half the env", extra: []string{"--stdio"}, env: map[string]string{"REX_BLOCK": "block:none"},
			wantRex: &host.RexBlock{BlockID: "block:none"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs, flags := newHostFlagSet(flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			args := append(append(append([]string{}, base...), tc.extra...), "--", "agent")
			if err := fs.Parse(args); err != nil {
				t.Fatalf("Parse = %v", err)
			}
			cfg, err := flags.serveConfig(fs.Args(), envOf(tc.env))
			if err != nil {
				t.Fatalf("serveConfig: %v", err)
			}
			if (cfg.Rex == nil) != (tc.wantRex == nil) || (tc.wantRex != nil && *cfg.Rex != *tc.wantRex) {
				t.Errorf("Rex = %+v, want %+v", cfg.Rex, tc.wantRex)
			}
		})
	}
}

// TestHostCmdPlanning: only the planning session's host is told it is one,
// which is what keeps its window labelled "plan" with no session prefix.
func TestHostCmdPlanning(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		{name: session.PlanSessionName, want: true},
		{name: "t4-3", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs, flags := newHostFlagSet(flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			if err := fs.Parse([]string{"--name", tc.name, "--", "agent"}); err != nil {
				t.Fatalf("Parse = %v", err)
			}
			cfg, err := flags.serveConfig(fs.Args(), noEnv)
			if err != nil {
				t.Fatalf("serveConfig: %v", err)
			}
			if cfg.Planning != tc.want {
				t.Errorf("Planning = %v, want %v", cfg.Planning, tc.want)
			}
		})
	}
}

// TestHostCmdWindowControl checks the Rex-backed WindowControl against
// rex.Fake: which hosts get one, and the calls it makes. Load-bearing rows:
// "afk runtime" and "stdio outside Rex" must make no Rex call ever (nil
// control); "window found once" proves the list_blocks lookup is cached, so
// a relabel costs one call; "rex missing" must say so in plain words before
// any call, which is the line the host logs when Rex is not installed; and
// "close self" closes the host's own block by its id.
func TestHostCmdWindowControl(t *testing.T) {
	stdioRex := host.ServeConfig{Stdio: true, Rex: &host.RexBlock{SessionID: "session:s1", BlockID: "block:b1"}}
	list := `{"blocks":[{"block_id":"block:b1","placement":"placed","window_id":"window:w1"}]}`
	onPath := func(string) (string, error) { return "/usr/bin/rex", nil }
	missing := func(string) (string, error) { return "", errors.New("not found") }
	inRex := envOf(map[string]string{"REX_BLOCK": "block:b1"})

	tests := []struct {
		name      string
		cfg       host.ServeConfig
		env       func(string) string
		lookPath  func(string) (string, error)
		do        func(w host.WindowControl) error
		wantNil   bool
		wantCalls []string
		wantArgs  []string
		wantErr   string
	}{
		{name: "afk runtime", cfg: host.ServeConfig{}, env: inRex, lookPath: onPath, wantNil: true},
		{name: "stdio outside Rex", cfg: stdioRex, env: noEnv, lookPath: onPath, wantNil: true},
		{name: "window found once", cfg: stdioRex, env: inRex, lookPath: onPath,
			do: func(w host.WindowControl) error {
				if err := w.SetLabel("t4-3 · build"); err != nil {
					return err
				}
				return w.SetLabel("t4-3 · review")
			},
			wantCalls: []string{"session.list_blocks", "session.set_window_label", "session.set_window_label"},
			wantArgs:  []string{"", `{"label":"t4-3 · build","session_id":"session:s1","window_id":"window:w1"}`, `{"label":"t4-3 · review","session_id":"session:s1","window_id":"window:w1"}`}},
		{name: "a given window id is used as it is",
			cfg: host.ServeConfig{Stdio: true, Rex: &host.RexBlock{SessionID: "session:s1", WindowID: "window:w9", BlockID: "block:b1"}},
			env: inRex, lookPath: onPath,
			do:        func(w host.WindowControl) error { return w.SetLabel("plan") },
			wantCalls: []string{"session.set_window_label"},
			wantArgs:  []string{`{"label":"plan","session_id":"session:s1","window_id":"window:w9"}`}},
		{name: "close self", cfg: stdioRex, env: inRex, lookPath: onPath,
			do:        func(w host.WindowControl) error { return w.CloseSelf() },
			wantCalls: []string{"block.close"},
			wantArgs:  []string{`{"block_id":"block:b1","session_id":"session:s1"}`}},
		{name: "rex missing", cfg: host.ServeConfig{Stdio: true, Rex: &host.RexBlock{BlockID: "block:none"}},
			env: inRex, lookPath: missing,
			do: func(w host.WindowControl) error {
				if err := w.SetLabel("x"); err == nil || !strings.Contains(err.Error(), "not on PATH") {
					return errors.New("SetLabel did not say rex is not on PATH: " + errString(err))
				}
				return w.CloseSelf()
			},
			wantErr: "the rex command is not on PATH"},
		{name: "block not in a window", cfg: stdioRex, env: inRex, lookPath: onPath,
			do:        func(w host.WindowControl) error { return w.SetLabel("x") },
			wantCalls: []string{"session.list_blocks"},
			wantErr:   "no window for block block:b1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := (&rex.Fake{}).
				On("session.set_window_label", `{}`, nil).
				On("block.close", `{}`, nil)
			if tc.name == "block not in a window" {
				f.On("session.list_blocks", `{"blocks":[]}`, nil)
			} else {
				f.On("session.list_blocks", list, nil)
			}
			w := hostWindowControl(tc.cfg, tc.env, f, tc.lookPath)
			if tc.wantNil {
				if w != nil {
					t.Fatalf("hostWindowControl = %#v, want nil", w)
				}
				return
			}
			if w == nil {
				t.Fatal("hostWindowControl = nil, want a control")
			}
			err := tc.do(w)
			if tc.wantErr == "" && err != nil {
				t.Fatalf("err = %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
			calls := f.Calls()
			var methods []string
			for i, c := range calls {
				methods = append(methods, c.Method)
				if i < len(tc.wantArgs) && tc.wantArgs[i] != "" && string(c.Args) != tc.wantArgs[i] {
					t.Errorf("call %d args = %s, want %s", i, c.Args, tc.wantArgs[i])
				}
			}
			if strings.Join(methods, " ") != strings.Join(tc.wantCalls, " ") {
				t.Errorf("calls = %q, want %q", methods, tc.wantCalls)
			}
		})
	}
}

func errString(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}
