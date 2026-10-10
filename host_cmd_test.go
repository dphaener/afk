package main

import (
	"flag"
	"io"
	"strings"
	"testing"
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
