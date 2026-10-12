package daemonconfig

import (
	"errors"
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"time"
)

func deps(env map[string]string, files map[string]string) Deps {
	return Deps{
		Getenv:  func(k string) string { return env[k] },
		HomeDir: func() (string, error) { return "/home/u", nil },
		ReadFile: func(p string) ([]byte, error) {
			if s, ok := files[p]; ok {
				return []byte(s), nil
			}
			return nil, fs.ErrNotExist
		},
	}
}

const home = "/home/u/.config/afk/config.toml"

// TestPath: the relative-XDG row is load-bearing. It is where this reader
// (spec rule, shared with usagestate) differs from internal/config, so a
// regression would silently split the two files across folders.
func TestPath(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"default", nil, home},
		{"absolute xdg", map[string]string{"XDG_CONFIG_HOME": "/x"}, "/x/afk/config.toml"},
		{"relative xdg ignored", map[string]string{"XDG_CONFIG_HOME": "rel"}, home},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Path(deps(c.env, nil)); got != c.want {
				t.Errorf("Path = %q, want %q", got, c.want)
			}
		})
	}
}

// TestLoad: the error rows each pin that the message names config.toml and
// the key, since the owner fixes the file from that message alone. The
// missing-file and unknown-key rows are load-bearing for first runs and for
// forward compatibility with keys later tasks add. The max_restarts = 0 row
// is load-bearing: zero is a real setting (never restart), so a reader that
// treated an absent key and a zero the same way would silently grant three
// restarts to someone who asked for none.
func TestLoad(t *testing.T) {
	cases := []struct {
		name    string
		toml    string
		missing bool
		want    Config
		errHas  []string
	}{
		{name: "missing file gives defaults", missing: true,
			want: Config{Tick: DefaultTick, StallAfter: DefaultStallAfter,
				MaxRestarts: DefaultMaxRestarts, RestartBackoff: DefaultRestartBackoff}},
		{name: "full file", toml: `
provider = "some-provider"
[daemon]
tick = "2s"
stall_after = "30m"
[[project]]
path = "~/code/a"
[[project]]
path = "/srv/b"
name = "bee"
`, want: Config{Provider: "some-provider", Tick: 2 * time.Second, StallAfter: 30 * time.Minute,
			MaxRestarts: DefaultMaxRestarts, RestartBackoff: DefaultRestartBackoff,
			Projects: []Project{{"a", "/home/u/code/a"}, {"bee", "/srv/b"}}}},
		{name: "defaults when daemon table absent", toml: "[[project]]\npath = \"/srv/b\"\n",
			want: Config{Tick: DefaultTick, StallAfter: DefaultStallAfter,
				MaxRestarts: DefaultMaxRestarts, RestartBackoff: DefaultRestartBackoff,
				Projects: []Project{{"b", "/srv/b"}}}},
		{name: "restart keys absent gives defaults", toml: "[daemon]\ntick = \"5s\"\n",
			want: Config{Tick: DefaultTick, StallAfter: DefaultStallAfter,
				MaxRestarts: DefaultMaxRestarts, RestartBackoff: DefaultRestartBackoff}},
		{name: "restart keys both set", toml: "[daemon]\nmax_restarts = 5\nrestart_backoff = \"30s\"\n",
			want: Config{Tick: DefaultTick, StallAfter: DefaultStallAfter,
				MaxRestarts: 5, RestartBackoff: 30 * time.Second}},
		{name: "max_restarts zero means never restart", toml: "[daemon]\nmax_restarts = 0\n",
			want: Config{Tick: DefaultTick, StallAfter: DefaultStallAfter,
				MaxRestarts: 0, RestartBackoff: DefaultRestartBackoff}},
		{name: "max_restarts negative", toml: "[daemon]\nmax_restarts = -1\n", errHas: []string{"config.toml", "daemon.max_restarts"}},
		{name: "restart_backoff not a duration", toml: "[daemon]\nrestart_backoff = \"soon\"\n", errHas: []string{"config.toml", "daemon.restart_backoff", "soon"}},
		{name: "restart_backoff zero", toml: "[daemon]\nrestart_backoff = \"0s\"\n", errHas: []string{"config.toml", "daemon.restart_backoff", "above zero"}},
		{name: "unknown keys ignored", toml: "[governor]\nx = 1\n[web]\nport = 3\nfuture = true\n",
			want: Config{Tick: DefaultTick, StallAfter: DefaultStallAfter,
				MaxRestarts: DefaultMaxRestarts, RestartBackoff: DefaultRestartBackoff}},
		{name: "bad tick duration", toml: "[daemon]\ntick = \"soon\"\n", errHas: []string{"config.toml", "daemon.tick", "soon"}},
		{name: "bad stall_after duration", toml: "[daemon]\nstall_after = \"10\"\n", errHas: []string{"config.toml", "daemon.stall_after"}},
		{name: "tick under one second", toml: "[daemon]\ntick = \"500ms\"\n", errHas: []string{"config.toml", "daemon.tick", "1s"}},
		{name: "relative project path", toml: "[[project]]\npath = \"code/a\"\n", errHas: []string{"config.toml", "project[0].path", "absolute"}},
		{name: "project without path", toml: "[[project]]\nname = \"a\"\n", errHas: []string{"config.toml", "project[0].path"}},
		{name: "duplicate name", toml: "[[project]]\npath = \"/x/a\"\n[[project]]\npath = \"/y/a\"\n", errHas: []string{"config.toml", "project[1].name", "\"a\""}},
		{name: "invalid toml", toml: "[[project\n", errHas: []string{"config.toml", "TOML"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files := map[string]string{}
			if !c.missing {
				files[home] = c.toml
			}
			got, err := Load(deps(nil, files))
			if len(c.errHas) > 0 {
				if err == nil {
					t.Fatalf("want error containing %v, got none", c.errHas)
				}
				for _, s := range c.errHas {
					if !strings.Contains(err.Error(), s) {
						t.Errorf("error %q lacks %q", err, s)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestLoadReadError(t *testing.T) {
	d := deps(nil, nil)
	d.ReadFile = func(string) ([]byte, error) { return nil, errors.New("boom") }
	if _, err := Load(d); err == nil || !strings.Contains(err.Error(), "config.toml") {
		t.Errorf("want error naming config.toml, got %v", err)
	}
}
