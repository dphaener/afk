package main

import (
	"flag"
	"io"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/host"
)

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
			cfg, err := flags.serveConfig(fs.Args())
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
	if cfg, err := flags.serveConfig(fs.Args()); err == nil {
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
			cfg, err := flags.serveConfig(fs.Args())
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
			cfg, err := flags.serveConfig(fs.Args())
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
