package daemonconfig

import (
	"errors"
	"io/fs"
	"path/filepath"
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

// TestPath: the relative-XDG row is load-bearing. The XDG spec says a relative
// value must be ignored, and internal/usagestate follows the same rule, so a
// regression here would silently put config.toml and usage.json in different
// folders.
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
// restarts to someone who asked for none. The max_wakes rows do the same job
// for wakes: max_wakes = 0 must read as 0 and not fall back to the default.
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
				MaxRestarts: DefaultMaxRestarts, RestartBackoff: DefaultRestartBackoff, MaxWakes: DefaultMaxWakes}},
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
			MaxRestarts: DefaultMaxRestarts, RestartBackoff: DefaultRestartBackoff, MaxWakes: DefaultMaxWakes,
			Projects: []Project{{"a", "/home/u/code/a"}, {"bee", "/srv/b"}}}},
		{name: "defaults when daemon table absent", toml: "[[project]]\npath = \"/srv/b\"\n",
			want: Config{Tick: DefaultTick, StallAfter: DefaultStallAfter,
				MaxRestarts: DefaultMaxRestarts, RestartBackoff: DefaultRestartBackoff, MaxWakes: DefaultMaxWakes,
				Projects: []Project{{"b", "/srv/b"}}}},
		{name: "restart keys absent gives defaults", toml: "[daemon]\ntick = \"5s\"\n",
			want: Config{Tick: DefaultTick, StallAfter: DefaultStallAfter,
				MaxRestarts: DefaultMaxRestarts, RestartBackoff: DefaultRestartBackoff, MaxWakes: DefaultMaxWakes}},
		{name: "restart keys both set", toml: "[daemon]\nmax_restarts = 5\nrestart_backoff = \"30s\"\n",
			want: Config{Tick: DefaultTick, StallAfter: DefaultStallAfter,
				MaxRestarts: 5, RestartBackoff: 30 * time.Second, MaxWakes: DefaultMaxWakes}},
		{name: "max_restarts zero means never restart", toml: "[daemon]\nmax_restarts = 0\n",
			want: Config{Tick: DefaultTick, StallAfter: DefaultStallAfter,
				MaxRestarts: 0, RestartBackoff: DefaultRestartBackoff, MaxWakes: DefaultMaxWakes}},
		{name: "max_restarts negative", toml: "[daemon]\nmax_restarts = -1\n", errHas: []string{"config.toml", "daemon.max_restarts"}},
		{name: "max_wakes set", toml: "[daemon]\nmax_wakes = 5\n",
			want: Config{Tick: DefaultTick, StallAfter: DefaultStallAfter,
				MaxRestarts: DefaultMaxRestarts, RestartBackoff: DefaultRestartBackoff, MaxWakes: 5}},
		{name: "max_wakes zero means never wake", toml: "[daemon]\nmax_wakes = 0\n",
			want: Config{Tick: DefaultTick, StallAfter: DefaultStallAfter,
				MaxRestarts: DefaultMaxRestarts, RestartBackoff: DefaultRestartBackoff, MaxWakes: 0}},
		{name: "max_wakes negative", toml: "[daemon]\nmax_wakes = -1\n", errHas: []string{"config.toml", "daemon.max_wakes"}},
		{name: "restart_backoff not a duration", toml: "[daemon]\nrestart_backoff = \"soon\"\n", errHas: []string{"config.toml", "daemon.restart_backoff", "soon"}},
		{name: "restart_backoff zero", toml: "[daemon]\nrestart_backoff = \"0s\"\n", errHas: []string{"config.toml", "daemon.restart_backoff", "above zero"}},
		{name: "unknown keys ignored", toml: "[governor]\nx = 1\n[web]\nport = 3\nfuture = true\n",
			want: Config{Tick: DefaultTick, StallAfter: DefaultStallAfter,
				MaxRestarts: DefaultMaxRestarts, RestartBackoff: DefaultRestartBackoff, MaxWakes: DefaultMaxWakes}},
		{name: "bad tick duration", toml: "[daemon]\ntick = \"soon\"\n", errHas: []string{"config.toml", "daemon.tick", "soon"}},
		{name: "bad stall_after duration", toml: "[daemon]\nstall_after = \"10\"\n", errHas: []string{"config.toml", "daemon.stall_after"}},
		{name: "tick under one second", toml: "[daemon]\ntick = \"500ms\"\n", errHas: []string{"config.toml", "daemon.tick", "1s"}},
		{name: "relative project path", toml: "[[project]]\npath = \"code/a\"\n", errHas: []string{"config.toml", "project[0].path", "absolute"}},
		{name: "project without path", toml: "[[project]]\nname = \"a\"\n", errHas: []string{"config.toml", "project[0].path"}},
		{name: "duplicate name", toml: "[[project]]\npath = \"/x/a\"\n[[project]]\npath = \"/y/a\"\n", errHas: []string{"config.toml", "project[1].name", "\"a\""}},
		{name: "governor defaults when table absent", toml: "[daemon]\ntick = \"5s\"\n",
			want: Config{Tick: DefaultTick, StallAfter: DefaultStallAfter,
				MaxRestarts: DefaultMaxRestarts, RestartBackoff: DefaultRestartBackoff, MaxWakes: DefaultMaxWakes, Governor: defGov}},
		{name: "governor every key set", toml: "[governor]\npause_at = 80\nlaunch_below = 60.5\nstale_after = \"20m\"\nprobe_interval = \"2m\"\non_unknown_usage = \"hold\"\n",
			want: Config{Tick: DefaultTick, StallAfter: DefaultStallAfter,
				MaxRestarts: DefaultMaxRestarts, RestartBackoff: DefaultRestartBackoff, MaxWakes: DefaultMaxWakes,
				Governor: Governor{PauseAt: 80, LaunchBelow: 60.5, StaleAfter: 20 * time.Minute,
					ProbeInterval: 2 * time.Minute, OnUnknownUsage: "hold"}}},
		{name: "governor pause_at 100 allowed", toml: "[governor]\npause_at = 100\n",
			want: Config{Tick: DefaultTick, StallAfter: DefaultStallAfter,
				MaxRestarts: DefaultMaxRestarts, RestartBackoff: DefaultRestartBackoff, MaxWakes: DefaultMaxWakes,
				Governor: Governor{PauseAt: 100, LaunchBelow: 75, StaleAfter: 30 * time.Minute,
					ProbeInterval: 5 * time.Minute, OnUnknownUsage: "run"}}},
		{name: "governor launch_below equals pause_at", toml: "[governor]\nlaunch_below = 90\n",
			want: Config{Tick: DefaultTick, StallAfter: DefaultStallAfter,
				MaxRestarts: DefaultMaxRestarts, RestartBackoff: DefaultRestartBackoff, MaxWakes: DefaultMaxWakes,
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
			if reflect.DeepEqual(want.Dash, Dash{}) {
				want.Dash = defaultDash()
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("got %+v, want %+v", got, want)
			}
		})
	}
}

// TestLoadDash: every row's file also holds a [daemon] tick and one
// [[project]], and every row checks that Load's error is nil and that both
// were read. That is the load-bearing part: the daemon and `afk project`,
// `afk usage`, `afk pause` and `afk inbox` share this load, so the rows with
// a bad value ("below the floor", "zero", "negative", "not a duration", the
// max_depth rows, the wrong-type rows, "dash is a number", "dash is a list of
// tables", "home unknown") are what stop a typo in a dashboard setting from
// stopping the build loop. Each of them must give a nil error, the defaults
// in Dash, and a DashErr naming the file and the key.
//
// The wrong-type rows are load-bearing for a second reason: a typed decode of
// the table would fail the whole file on them, with an error that never
// reaches loadDash. "at the floor" pins that the limit is inclusive.
// The leader rows pin that a non-string leader is refused like any other bad
// value, that a string is kept as written (trimmed, never checked here, so a
// mistyped key name cannot reset the folder search) and that it does not
// disturb the other keys.
// "max_depth zero" pins that a written 0 is refused and not passed on as
// "not set" (see Dash.MaxDepth). "relative root" pins that a relative folder
// stays allowed, unlike a [[project]] path.
func TestLoadDash(t *testing.T) {
	rel, err := filepath.Abs("code")
	if err != nil {
		t.Fatal(err)
	}
	def := defaultDash()
	cases := []struct {
		name   string
		toml   string
		noHome bool
		want   Dash
		errHas []string // non-empty: DashErr must contain each, Dash is the defaults
	}{
		{name: "absent table", toml: "", want: def},
		{name: "empty table", toml: "[dash]\n", want: def},
		{name: "search_roots alone", toml: "[dash]\nsearch_roots = [\"/srv/code\"]\n",
			want: Dash{SearchRoots: []string{"/srv/code"}, ScanInterval: DefaultScanInterval, MaxDepth: DefaultMaxDepth}},
		{name: "scan_interval alone", toml: "[dash]\nscan_interval = \"30s\"\n",
			want: Dash{ScanInterval: 30 * time.Second, MaxDepth: DefaultMaxDepth}},
		{name: "max_depth alone", toml: "[dash]\nmax_depth = 5\n",
			want: Dash{ScanInterval: DefaultScanInterval, MaxDepth: 5}},
		{name: "all three", toml: "[dash]\nsearch_roots = [\"/srv/code\", \"/srv/work\"]\nscan_interval = \"2m\"\nmax_depth = 1\n",
			want: Dash{SearchRoots: []string{"/srv/code", "/srv/work"}, ScanInterval: 2 * time.Minute, MaxDepth: 1}},
		{name: "empty search_roots", toml: "[dash]\nsearch_roots = []\n", want: def},
		{name: "unknown key ignored", toml: "[dash]\nfuture = true\nmax_depth = 2\n",
			want: Dash{ScanInterval: DefaultScanInterval, MaxDepth: 2}},
		{name: "scan_interval at the floor", toml: "[dash]\nscan_interval = \"1s\"\n",
			want: Dash{ScanInterval: time.Second, MaxDepth: DefaultMaxDepth}},
		{name: "scan_interval below the floor", toml: "[dash]\nscan_interval = \"999ms\"\n",
			errHas: []string{home, "dash.scan_interval", "1s"}},
		{name: "scan_interval zero", toml: "[dash]\nscan_interval = \"0s\"\n",
			errHas: []string{home, "dash.scan_interval", "1s"}},
		{name: "scan_interval negative", toml: "[dash]\nscan_interval = \"-5s\"\n",
			errHas: []string{home, "dash.scan_interval", "1s"}},
		{name: "scan_interval not a duration", toml: "[dash]\nscan_interval = \"soon\"\n",
			errHas: []string{home, "dash.scan_interval", "soon", "not a duration"}},
		{name: "scan_interval of the wrong type", toml: "[dash]\nscan_interval = 60\n",
			errHas: []string{home, "dash.scan_interval", "60s"}},
		{name: "max_depth zero", toml: "[dash]\nmax_depth = 0\n",
			errHas: []string{home, "dash.max_depth", "1 or more"}},
		{name: "max_depth negative", toml: "[dash]\nmax_depth = -2\n",
			errHas: []string{home, "dash.max_depth", "1 or more"}},
		{name: "max_depth of the wrong type", toml: "[dash]\nmax_depth = \"three\"\n",
			errHas: []string{home, "dash.max_depth", "three", "whole number"}},
		{name: "max_depth with a fraction", toml: "[dash]\nmax_depth = 2.5\n",
			errHas: []string{home, "dash.max_depth", "whole number"}},
		{name: "a bad key beside good ones gives the defaults", toml: "[dash]\nsearch_roots = [\"/srv/code\"]\nscan_interval = \"30s\"\nmax_depth = 0\n",
			errHas: []string{home, "dash.max_depth"}},
		{name: "tilde slash expanded", toml: "[dash]\nsearch_roots = [\"~/code\"]\n",
			want: Dash{SearchRoots: []string{"/home/u/code"}, ScanInterval: DefaultScanInterval, MaxDepth: DefaultMaxDepth}},
		{name: "bare tilde expanded", toml: "[dash]\nsearch_roots = [\"~\"]\n",
			want: Dash{SearchRoots: []string{"/home/u"}, ScanInterval: DefaultScanInterval, MaxDepth: DefaultMaxDepth}},
		{name: "absolute root cleaned", toml: "[dash]\nsearch_roots = [\"/srv//code/\"]\n",
			want: Dash{SearchRoots: []string{"/srv/code"}, ScanInterval: DefaultScanInterval, MaxDepth: DefaultMaxDepth}},
		{name: "relative root made absolute", toml: "[dash]\nsearch_roots = [\"code\"]\n",
			want: Dash{SearchRoots: []string{rel}, ScanInterval: DefaultScanInterval, MaxDepth: DefaultMaxDepth}},
		{name: "home unknown with a tilde root", toml: "[dash]\nsearch_roots = [\"/srv/code\", \"~/code\"]\n", noHome: true,
			errHas: []string{"/x/afk/config.toml", "dash.search_roots[1]", "home"}},
		{name: "home unknown without a tilde root", toml: "[dash]\nsearch_roots = [\"/srv/code\"]\n", noHome: true,
			want: Dash{SearchRoots: []string{"/srv/code"}, ScanInterval: DefaultScanInterval, MaxDepth: DefaultMaxDepth}},
		{name: "search_roots not a list", toml: "[dash]\nsearch_roots = \"/srv/code\"\n",
			errHas: []string{home, "dash.search_roots", "list"}},
		{name: "search_roots holds a number", toml: "[dash]\nsearch_roots = [\"/srv/code\", 7]\n",
			errHas: []string{home, "dash.search_roots[1]"}},
		{name: "leader alone", toml: "[dash]\nleader = \"ctrl+o\"\n",
			want: Dash{ScanInterval: DefaultScanInterval, MaxDepth: DefaultMaxDepth, Leader: "ctrl+o"}},
		{name: "leader trimmed", toml: "[dash]\nleader = \"  ctrl+o  \"\n",
			want: Dash{ScanInterval: DefaultScanInterval, MaxDepth: DefaultMaxDepth, Leader: "ctrl+o"}},
		{name: "leader empty", toml: "[dash]\nleader = \"\"\n", want: def},
		{name: "leader unchecked here", toml: "[dash]\nleader = \"enter\"\n",
			want: Dash{ScanInterval: DefaultScanInterval, MaxDepth: DefaultMaxDepth, Leader: "enter"}},
		{name: "leader beside search_roots", toml: "[dash]\nleader = \"alt+x\"\nsearch_roots = [\"/srv/code\"]\n",
			want: Dash{SearchRoots: []string{"/srv/code"}, ScanInterval: DefaultScanInterval, MaxDepth: DefaultMaxDepth, Leader: "alt+x"}},
		{name: "leader of the wrong type", toml: "[dash]\nleader = 5\n",
			errHas: []string{home, "dash.leader", "ctrl+o"}},
		{name: "dash is a number", toml: "dash = 5\n",
			errHas: []string{home, "dash", "[dash]"}},
		{name: "dash is a list of tables", toml: "[[dash]]\nmax_depth = 2\n",
			errHas: []string{home, "dash", "[dash]"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// The top-level key of the "dash is a number" row has to come
			// before any table, so the shared tables go after the row's text.
			text := c.toml + "\n[daemon]\ntick = \"2s\"\n\n[[project]]\npath = \"/srv/b\"\n"
			d := deps(nil, map[string]string{home: text})
			if c.noHome {
				// With no home the file is found through an absolute
				// XDG_CONFIG_HOME, the only way Load can still find it.
				d = deps(map[string]string{"XDG_CONFIG_HOME": "/x"}, map[string]string{"/x/afk/config.toml": text})
				d.HomeDir = func() (string, error) { return "", errors.New("no home") }
			}
			got, err := Load(d)
			if err != nil {
				t.Fatalf("Load returned an error over a [dash] value: %v", err)
			}
			if got.Tick != 2*time.Second || !reflect.DeepEqual(got.Projects, []Project{{"b", "/srv/b"}}) {
				t.Errorf("the rest of the file was not read: tick %s, projects %v", got.Tick, got.Projects)
			}
			if len(c.errHas) > 0 {
				if got.DashErr == nil {
					t.Fatalf("want DashErr containing %v, got none (Dash %+v)", c.errHas, got.Dash)
				}
				for _, s := range c.errHas {
					if !strings.Contains(got.DashErr.Error(), s) {
						t.Errorf("DashErr %q lacks %q", got.DashErr, s)
					}
				}
				if !strings.HasPrefix(got.DashErr.Error(), Path(d)+": ") {
					t.Errorf("DashErr %q does not start with the file's path", got.DashErr)
				}
				if !reflect.DeepEqual(got.Dash, def) {
					t.Errorf("Dash = %+v on a bad value, want the defaults %+v", got.Dash, def)
				}
				return
			}
			if got.DashErr != nil {
				t.Fatalf("DashErr = %v, want none", got.DashErr)
			}
			if !reflect.DeepEqual(got.Dash, c.want) {
				t.Errorf("Dash = %+v, want %+v", got.Dash, c.want)
			}
		})
	}
}

// TestLoadDashMissingFile: a machine with no config.toml still gets the [dash]
// defaults, so the dashboard has an interval and a depth to work with.
func TestLoadDashMissingFile(t *testing.T) {
	got, err := Load(deps(nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got.DashErr != nil || !reflect.DeepEqual(got.Dash, defaultDash()) {
		t.Errorf("Dash = %+v, DashErr = %v; want the defaults and no error", got.Dash, got.DashErr)
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
