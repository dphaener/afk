package daemonconfig

import (
	"errors"
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/governor"
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

// TestLoad: rows that do not set a Governor expect the governor defaults (the
// loop fills them in), so the older rows keep pinning only what they test.
// The governor rows are load-bearing as follows: "defaults" pins the single
// source of the numbers; "pause_at 100" and "launch_below equals pause_at"
// pin the inclusive upper bounds; "stale_after equal to probe_interval" pins
// the strict inequality that stops readings going stale between probes; the
// zero rows pin that a written 0 is an error and not read as absent. The error
// rows each pin that the message names config.toml and
// the key, since the owner fixes the file from that message alone. The
// missing-file and unknown-key rows are load-bearing for first runs and for
// forward compatibility with keys later tasks add. The max_restarts = 0 row
// is load-bearing: zero is a real setting (never restart), so a reader that
// treated an absent key and a zero the same way would silently grant three
// restarts to someone who asked for none.
func TestLoad(t *testing.T) {
	defGov := Governor{PauseAt: 90, LaunchBelow: 75, StaleAfter: 30 * time.Minute,
		ProbeInterval: 5 * time.Minute, OnUnknownUsage: "run"}
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
		{name: "governor defaults when table absent", toml: "[daemon]\ntick = \"5s\"\n",
			want: Config{Tick: DefaultTick, StallAfter: DefaultStallAfter,
				MaxRestarts: DefaultMaxRestarts, RestartBackoff: DefaultRestartBackoff, Governor: defGov}},
		{name: "governor every key set", toml: "[governor]\npause_at = 80\nlaunch_below = 60.5\nstale_after = \"20m\"\nprobe_interval = \"2m\"\non_unknown_usage = \"hold\"\n",
			want: Config{Tick: DefaultTick, StallAfter: DefaultStallAfter,
				MaxRestarts: DefaultMaxRestarts, RestartBackoff: DefaultRestartBackoff,
				Governor: Governor{PauseAt: 80, LaunchBelow: 60.5, StaleAfter: 20 * time.Minute,
					ProbeInterval: 2 * time.Minute, OnUnknownUsage: "hold"}}},
		{name: "governor pause_at 100 allowed", toml: "[governor]\npause_at = 100\n",
			want: Config{Tick: DefaultTick, StallAfter: DefaultStallAfter,
				MaxRestarts: DefaultMaxRestarts, RestartBackoff: DefaultRestartBackoff,
				Governor: Governor{PauseAt: 100, LaunchBelow: 75, StaleAfter: 30 * time.Minute,
					ProbeInterval: 5 * time.Minute, OnUnknownUsage: "run"}}},
		{name: "governor launch_below equals pause_at", toml: "[governor]\nlaunch_below = 90\n",
			want: Config{Tick: DefaultTick, StallAfter: DefaultStallAfter,
				MaxRestarts: DefaultMaxRestarts, RestartBackoff: DefaultRestartBackoff,
				Governor: Governor{PauseAt: 90, LaunchBelow: 90, StaleAfter: 30 * time.Minute,
					ProbeInterval: 5 * time.Minute, OnUnknownUsage: "run"}}},
		{name: "governor pause_at 3", toml: "[governor]\npause_at = 3\n", errHas: []string{"config.toml", "governor.pause_at"}},
		{name: "governor pause_at 5", toml: "[governor]\npause_at = 5\n", errHas: []string{"governor.pause_at"}},
		{name: "governor pause_at zero", toml: "[governor]\npause_at = 0\n", errHas: []string{"governor.pause_at"}},
		{name: "governor pause_at over 100", toml: "[governor]\npause_at = 101\n", errHas: []string{"governor.pause_at"}},
		{name: "governor launch_below zero", toml: "[governor]\nlaunch_below = 0\n", errHas: []string{"governor.launch_below"}},
		{name: "governor launch_below above pause_at", toml: "[governor]\npause_at = 80\nlaunch_below = 81\n", errHas: []string{"governor.launch_below", "governor.pause_at"}},
		{name: "governor stale_after zero", toml: "[governor]\nstale_after = \"0s\"\n", errHas: []string{"governor.stale_after"}},
		{name: "governor stale_after negative", toml: "[governor]\nstale_after = \"-5m\"\n", errHas: []string{"governor.stale_after"}},
		{name: "governor stale_after not a duration", toml: "[governor]\nstale_after = \"30\"\n", errHas: []string{"governor.stale_after", "not a duration"}},
		{name: "governor probe_interval not a duration", toml: "[governor]\nprobe_interval = \"often\"\n", errHas: []string{"governor.probe_interval", "often"}},
		{name: "governor probe_interval under a minute", toml: "[governor]\nprobe_interval = \"30s\"\n", errHas: []string{"governor.probe_interval", "1m"}},
		{name: "governor stale_after equal to probe_interval", toml: "[governor]\nstale_after = \"5m\"\n", errHas: []string{"governor.stale_after", "governor.probe_interval"}},
		{name: "governor stale_after below probe_interval", toml: "[governor]\nprobe_interval = \"40m\"\n", errHas: []string{"governor.stale_after", "governor.probe_interval"}},
		{name: "governor on_unknown_usage maybe", toml: "[governor]\non_unknown_usage = \"maybe\"\n", errHas: []string{"governor.on_unknown_usage", "maybe"}},
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
			want := c.want
			if want.Governor == (Governor{}) {
				want.Governor = defGov
			}
			if want.Web == (Web{}) {
				want.Web = Web{Bind: DefaultWebBind}
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("got %+v, want %+v", got, want)
			}
		})
	}
}

// TestGovernorThresholds: the method must hand governor.Decide exactly the
// four limits it reads; ProbeInterval is the daemon's, not Decide's.
func TestGovernorThresholds(t *testing.T) {
	cfg, err := Load(deps(nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.Governor.Thresholds(), governor.DefaultThresholds(); got != want {
		t.Errorf("Thresholds = %+v, want %+v", got, want)
	}
	if cfg.Governor.ProbeInterval != governor.DefaultProbeInterval {
		t.Errorf("ProbeInterval = %s, want %s", cfg.Governor.ProbeInterval, governor.DefaultProbeInterval)
	}
}

func TestLoadReadError(t *testing.T) {
	d := deps(nil, nil)
	d.ReadFile = func(string) ([]byte, error) { return nil, errors.New("boom") }
	if _, err := Load(d); err == nil || !strings.Contains(err.Error(), "config.toml") {
		t.Errorf("want error naming config.toml, got %v", err)
	}
}

// TestLoadWeb: the non-loopback refusal rows and the ":4253" row are
// load-bearing. They are the only thing between a typo'd bind and a page that
// pauses the build loop for anyone on the network; ":4253" is the easy
// mistake because Go reads it as every interface. The 5-character token row
// also checks the error never repeats the secret.
func TestLoadWeb(t *testing.T) {
	const tok = "0123456789abcdef"
	cases := []struct {
		name   string
		toml   string
		want   Web
		errHas []string
		errNot []string
	}{
		{name: "no web table", toml: "", want: Web{Bind: "127.0.0.1:4253"}},
		{name: "token with default bind", toml: "[web]\ntoken = \"" + tok + "\"\n",
			want: Web{Bind: "127.0.0.1:4253", Token: tok}},
		{name: "0.0.0.0 no token", toml: "[web]\nbind = \"0.0.0.0:4253\"\n", errHas: []string{"config.toml", "web.bind", "web.token"}},
		{name: "empty host no token", toml: "[web]\nbind = \":4253\"\n", errHas: []string{"config.toml", "web.bind", "web.token"}},
		{name: "ipv6 any no token", toml: "[web]\nbind = \"[::]:4253\"\n", errHas: []string{"config.toml", "web.bind", "web.token"}},
		{name: "hostname no token", toml: "[web]\nbind = \"myhost:4253\"\n", errHas: []string{"config.toml", "web.bind", "web.token"}},
		{name: "0.0.0.0 with token", toml: "[web]\nbind = \"0.0.0.0:4253\"\ntoken = \"" + tok + "\"\n",
			want: Web{Bind: "0.0.0.0:4253", Token: tok}},
		{name: "empty host with token", toml: "[web]\nbind = \":4253\"\ntoken = \"" + tok + "\"\n",
			want: Web{Bind: ":4253", Token: tok}},
		{name: "ipv6 any with token", toml: "[web]\nbind = \"[::]:4253\"\ntoken = \"" + tok + "\"\n",
			want: Web{Bind: "[::]:4253", Token: tok}},
		{name: "hostname with token", toml: "[web]\nbind = \"myhost:4253\"\ntoken = \"" + tok + "\"\n",
			want: Web{Bind: "myhost:4253", Token: tok}},
		{name: "127.0.0.1 no token", toml: "[web]\nbind = \"127.0.0.1:9000\"\n", want: Web{Bind: "127.0.0.1:9000"}},
		{name: "ipv6 loopback no token", toml: "[web]\nbind = \"[::1]:9000\"\n", want: Web{Bind: "[::1]:9000"}},
		{name: "localhost no token", toml: "[web]\nbind = \"localhost:9000\"\n", want: Web{Bind: "localhost:9000"}},
		{name: "bind without port", toml: "[web]\nbind = \"127.0.0.1\"\n", errHas: []string{"config.toml", "web.bind"}},
		{name: "port zero", toml: "[web]\nbind = \"127.0.0.1:0\"\n", errHas: []string{"config.toml", "web.bind", "65535"}},
		{name: "port too big", toml: "[web]\nbind = \"127.0.0.1:70000\"\n", errHas: []string{"config.toml", "web.bind", "65535"}},
		{name: "port not a number", toml: "[web]\nbind = \"127.0.0.1:http\"\n", errHas: []string{"web.bind"}},
		{name: "short token", toml: "[web]\ntoken = \"abcde\"\n", errHas: []string{"config.toml", "web.token"}, errNot: []string{"abcde"}},
		{name: "token with space", toml: "[web]\ntoken = \"0123456789 abcdefXYZ\"\n", errHas: []string{"web.token", "white space"}, errNot: []string{"abcdefXYZ"}},
		{name: "public_url ftp", toml: "[web]\npublic_url = \"ftp://x\"\n", errHas: []string{"config.toml", "web.public_url"}},
		{name: "public_url bare host", toml: "[web]\npublic_url = \"example.com\"\n", errHas: []string{"web.public_url"}},
		{name: "public_url with query", toml: "[web]\npublic_url = \"https://afk.example/?a=b\"\n", errHas: []string{"web.public_url"}},
		{name: "public_url with fragment", toml: "[web]\npublic_url = \"https://afk.example/#x\"\n", errHas: []string{"web.public_url"}},
		{name: "public_url without host", toml: "[web]\npublic_url = \"https://\"\n", errHas: []string{"web.public_url"}},
		{name: "public_url trailing slash trimmed", toml: "[web]\npublic_url = \"https://afk.example/\"\n",
			want: Web{Bind: "127.0.0.1:4253", PublicURL: "https://afk.example"}},
		{name: "public_url http with port", toml: "[web]\npublic_url = \"http://10.0.0.2:4253\"\n",
			want: Web{Bind: "127.0.0.1:4253", PublicURL: "http://10.0.0.2:4253"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Load(deps(nil, map[string]string{home: c.toml}))
			if len(c.errHas) > 0 {
				if err == nil {
					t.Fatalf("want error containing %v, got none", c.errHas)
				}
				for _, s := range c.errHas {
					if !strings.Contains(err.Error(), s) {
						t.Errorf("error %q lacks %q", err, s)
					}
				}
				for _, s := range c.errNot {
					if strings.Contains(err.Error(), s) {
						t.Errorf("error %q repeats %q", err, s)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Web != c.want {
				t.Errorf("Web = %+v, want %+v", got.Web, c.want)
			}
			if got.Web.Enabled() != (c.want.Token != "") {
				t.Errorf("Enabled = %v with token %q", got.Web.Enabled(), c.want.Token)
			}
		})
	}
}

// TestLoopbackBind: the 0.0.0.0, "::", empty-host and hostname rows are
// load-bearing; each listens beyond this machine or may resolve to something
// that does, so none may pass for loopback.
func TestLoopbackBind(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:4253", true},
		{"127.5.5.5:1", true},
		{"[::1]:4253", true},
		{"localhost:4253", true},
		{"0.0.0.0:4253", false},
		{"[::]:4253", false},
		{":4253", false},
		{"myhost:4253", false},
		{"192.168.1.5:4253", false},
		{"127.0.0.1", false},
		{"", false},
	}
	for _, c := range cases {
		t.Run(c.addr, func(t *testing.T) {
			if got := LoopbackBind(c.addr); got != c.want {
				t.Errorf("LoopbackBind(%q) = %v, want %v", c.addr, got, c.want)
			}
		})
	}
}
