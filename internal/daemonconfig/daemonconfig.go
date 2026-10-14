// Package daemonconfig reads the machine's settings: config.toml in afk's
// config folder.
//
// It is the machine's only settings file. docs/PLAN.md §2 chose TOML for
// settings a person edits by hand, and T6.2 moved the dashboard's search
// settings here so there is one file to find and one format to learn. The
// package keeps its name because the build daemon was its first reader.
//
// Folder rule: $XDG_CONFIG_HOME/afk when that variable is an absolute path,
// otherwise ~/.config/afk. That is the rule internal/usagestate uses and the
// one the XDG spec gives (a relative value must be ignored), so config.toml
// sits next to usage.json.
//
// The [dash] table (search_roots, scan_interval, max_depth) is read here for
// the dashboard's folder search. The daemon ignores it, and so does every
// other command that shares this load: a bad [dash] value never fails Load.
// It is carried in Config.DashErr for the dashboard to report (see DashErr).
//
// The [governor] table holds the usage limits (pause_at, launch_below,
// stale_after, probe_interval, on_unknown_usage). An absent key takes its
// default from internal/governor, so the numbers have one source. They live
// here and not in a project's afk.toml because usage belongs to the account,
// which every project on the machine shares.
//
// The [web] table (bind, token, public_url) configures the daemon's web page.
// This package only reads and checks it; it opens no listener.
//
// Unknown keys are ignored: later tasks add keys to the same file, and an
// older daemon must not refuse a file a newer one wrote.
package daemonconfig

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/BurntSushi/toml"

	"github.com/dphaener/afk/internal/governor"
)

const (
	// DefaultTick is how often the daemon looks at each project.
	DefaultTick = 5 * time.Second
	// DefaultStallAfter is how long a running session may go without any
	// status change before the daemon calls it stalled.
	DefaultStallAfter = 10 * time.Minute
	// DefaultMaxRestarts is how many automatic recoveries (a restart of a
	// wedged agent, or a relaunch of a gone host) the daemon makes for one
	// task before it parks the task with a failure item (docs/PLAN.md §5.6).
	// Three rides out a one-off hang or a reboot, and is few enough that a
	// session which keeps dying reaches a person quickly.
	DefaultMaxRestarts = 3
	// DefaultRestartBackoff is the wait before the second recovery; each
	// later one waits twice as long as the one before (docs/PLAN.md §5.6).
	// The first recovery has no wait. A restart stops a working agent, so a
	// cause that keeps coming back must not be answered in a tight loop.
	DefaultRestartBackoff = time.Minute
	// DefaultMaxWakes is how many times the daemon types a session's resume
	// command into it for one task before it parks the task with a plain
	// item (docs/PLAN.md T2.5). Three rides out a provider blip that clears
	// within a few minutes, and is few enough that a session which keeps
	// failing reaches a person quickly.
	DefaultMaxWakes = 3

	fileName = "config.toml"
	// minTick keeps a typo such as "5ms" from turning the loop into a
	// busy-wait over git and the filesystem.
	minTick = time.Second
)

// Deps is the injectable environment, shaped like internal/usagestate's so
// tests never touch the real home or config folder.
type Deps struct {
	// Getenv looks up an environment variable, empty string if unset.
	Getenv func(string) string
	// HomeDir returns the user's home directory.
	HomeDir func() (string, error)
	// ReadFile reads a file. Nil means os.ReadFile.
	ReadFile func(string) ([]byte, error)
}

// DefaultDeps returns Deps wired to the real environment.
func DefaultDeps() Deps {
	return Deps{Getenv: os.Getenv, HomeDir: os.UserHomeDir, ReadFile: os.ReadFile}
}

// Project is one project the daemon builds.
type Project struct {
	Name string
	Path string
}

// Governor is the usage limits from the [governor] table. See
// internal/governor for what each one does to a decision.
type Governor struct {
	// PauseAt is the percent of a window at which running work stops.
	PauseAt float64
	// LaunchBelow is the percent under which new work may start.
	LaunchBelow float64
	// StaleAfter is how old a usage reading may be before it is not trusted.
	StaleAfter time.Duration
	// ProbeInterval is how often the daemon refreshes the reading.
	ProbeInterval time.Duration
	// OnUnknownUsage is governor.UnknownRun or governor.UnknownHold: what to
	// do when the tool cannot report usage at all.
	OnUnknownUsage string
}

// Thresholds returns the limits in the form governor.Decide takes.
func (g Governor) Thresholds() governor.Thresholds {
	return governor.Thresholds{PauseAt: g.PauseAt, LaunchBelow: g.LaunchBelow,
		StaleAfter: g.StaleAfter, OnUnknownUsage: g.OnUnknownUsage}
}

// DefaultWebBind is where the web page listens when [web] gives no bind: the
// loopback address, so the page is not reachable from other machines unless
// the owner asks for it.
const DefaultWebBind = "127.0.0.1:4253"

// minTokenLen is the shortest token accepted. A shorter secret is too easy to
// guess for a page that can answer inbox items and pause the build loop.
const minTokenLen = 16

// Web is the [web] table.
//
// An empty Token means the page is off, not open: PLAN T3.1 requires every
// request to carry the configured token, so with no token there is nothing a
// request could present, and serving anyway would serve anyone.
type Web struct {
	// Bind is the host:port to listen on. Never empty after Load.
	Bind string
	// Token is the shared secret every request must carry. Empty = page off.
	Token string
	// PublicURL is the address the owner reaches the page at, with no
	// trailing slash; empty when not set. Notifications link to it.
	PublicURL string
}

// Enabled reports whether the web page should be started.
func (w Web) Enabled() bool { return w.Token != "" }

// LoopbackBind reports whether addr (host:port) listens on this machine only.
// It is true only for an IP literal that IsLoopback accepts, or the name
// "localhost". An empty host, 0.0.0.0 and :: listen on every interface, and
// any other hostname is false because a name may resolve to anything,
// including a public address, so it cannot be trusted to stay local.
func LoopbackBind(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Config is the parsed machine config.
type Config struct {
	// Provider is a name handed to provider.Lookup by the caller; empty
	// means the default. It is not validated here.
	Provider   string
	Tick       time.Duration
	StallAfter time.Duration
	// MaxRestarts is the number of automatic recoveries allowed per task.
	// Zero is valid and means no session is ever restarted: the task parks
	// at once when its session stops answering.
	MaxRestarts int
	// RestartBackoff is the wait before the second recovery; it doubles for
	// each recovery after that. Always above zero.
	RestartBackoff time.Duration
	// MaxWakes is the number of times the daemon wakes a session for one
	// task. Zero is valid and means no session is ever woken: a stall parks
	// the task at once.
	MaxWakes int
	Projects []Project
	// Governor is always filled: absent keys carry the governor defaults.
	Governor Governor
	// Web is always filled: an absent table carries the default bind and no
	// token, so the page is off.
	Web Web
	// Dash is always filled: an absent [dash] table, and a [dash] table with
	// any bad value in it, both carry the defaults (no search folders).
	Dash Dash
	// DashErr is what is wrong with the [dash] table, or nil. It names the
	// file and the key.
	//
	// It is carried here and not returned by Load because the daemon and
	// `afk project`, `afk usage`, `afk pause` and `afk inbox` all share this
	// one load, and each of them refuses to go on when Load fails. The [dash]
	// table only matters to the dashboard, and docs/PLAN.md T6.2 forbids the
	// daemon failing over a [dash] value: a typo in a search setting must not
	// stop the build loop or make an unrelated command refuse. So only the
	// dashboard looks at this field, and everyone else never sees the error.
	DashErr error
}

// The [dash] defaults: how often the dashboard looks through the search
// folders, and how many folders deep it looks below each one.
const (
	DefaultScanInterval = 60 * time.Second
	DefaultMaxDepth     = 3
)

// minScanInterval is the least dash.scan_interval accepted. A scan walks every
// search folder and runs `git rev-parse` for each repository candidate it
// finds (internal/scan), so an interval shorter than this arms the next tick
// before a walk over anything but a trivial tree can have finished, and the
// disk walks stack up instead of one replacing the last. One second is far
// above a typo such as "1ms", the case this guards against, and still far
// below the 60s default, which leaves room for someone who wants a quicker
// switcher over a small set of folders.
const minScanInterval = time.Second

// Dash is the [dash] table: where the dashboard looks for projects that are
// not listed as a [[project]].
type Dash struct {
	// SearchRoots are the folders to look through, each an absolute path. Nil
	// when none is set, which turns the search off.
	SearchRoots []string
	// ScanInterval is the wait between two searches. Never below one second.
	ScanInterval time.Duration
	// MaxDepth is how many folders deep the search goes below each root.
	//
	// Never below 1 after Load, and a written 0 or negative number is refused
	// instead of being passed on. internal/scan's Deps.MaxDepth is a plain int
	// that reads any value below 1 as "not set" and replaces it with its own
	// default, so a 0 handed on from here would silently become a different
	// number from the one the owner wrote. The key being absent and the key
	// being 0 can only be told apart in this loader, so this is where the 0
	// is caught.
	MaxDepth int
}

func defaultDash() Dash {
	return Dash{ScanInterval: DefaultScanInterval, MaxDepth: DefaultMaxDepth}
}

type rawFile struct {
	Provider string `toml:"provider"`
	Daemon   struct {
		Tick       string `toml:"tick"`
		StallAfter string `toml:"stall_after"`
		// MaxRestarts is a pointer so an absent key (use the default) can be
		// told apart from max_restarts = 0 (never restart).
		MaxRestarts    *int   `toml:"max_restarts"`
		RestartBackoff string `toml:"restart_backoff"`
		// MaxWakes is a pointer for the same reason as MaxRestarts: an absent
		// key must not read as max_wakes = 0 (never wake).
		MaxWakes *int `toml:"max_wakes"`
	} `toml:"daemon"`
	Governor struct {
		// Numbers are pointers so an absent key (use the default) can be told
		// apart from a written zero, which is then rejected by name.
		PauseAt        *float64 `toml:"pause_at"`
		LaunchBelow    *float64 `toml:"launch_below"`
		StaleAfter     string   `toml:"stale_after"`
		ProbeInterval  string   `toml:"probe_interval"`
		OnUnknownUsage string   `toml:"on_unknown_usage"`
	} `toml:"governor"`
	Web struct {
		Bind      string `toml:"bind"`
		Token     string `toml:"token"`
		PublicURL string `toml:"public_url"`
	} `toml:"web"`
	// Dash is left as whatever TOML value was written, and loadDash checks
	// its shape and types by hand. A typed struct here would turn
	// max_depth = "three", or dash = 5, into a decode error for the whole
	// file, and that would fail Load for the daemon over a dashboard setting.
	// The decoder fills an `any` with plain maps, slices, strings and numbers,
	// which reflect.DeepEqual compares by value, so the "nothing but the
	// projects changed" check in edit.go still covers this table.
	Dash    any `toml:"dash"`
	Project []struct {
		Path string `toml:"path"`
		Name string `toml:"name"`
	} `toml:"project"`
}

// Path returns where config.toml lives, or "" when neither XDG_CONFIG_HOME
// nor the home directory can be determined (Load reports that).
func Path(d Deps) string {
	if x := d.Getenv("XDG_CONFIG_HOME"); x != "" && filepath.IsAbs(x) {
		return filepath.Join(x, "afk", fileName)
	}
	home, err := d.HomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "afk", fileName)
}

// Load reads config.toml. A missing file is not an error: it yields the
// defaults and no projects, so a fresh machine can run `afk daemon status`.
func Load(d Deps) (Config, error) {
	cfg := Config{Tick: DefaultTick, StallAfter: DefaultStallAfter,
		MaxRestarts: DefaultMaxRestarts, RestartBackoff: DefaultRestartBackoff,
		MaxWakes: DefaultMaxWakes, Governor: defaultGovernor(), Web: Web{Bind: DefaultWebBind},
		Dash: defaultDash()}
	path := Path(d)
	if path == "" {
		return cfg, errors.New("cannot find afk's config folder: set XDG_CONFIG_HOME to an absolute path or make sure your home directory is known")
	}
	read := d.ReadFile
	if read == nil {
		read = os.ReadFile
	}
	data, err := read(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("%s: cannot read the file: %w", path, err)
	}
	var raw rawFile
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return cfg, fmt.Errorf("%s: not valid TOML, fix the syntax: %w", path, err)
	}

	cfg.Provider = strings.TrimSpace(raw.Provider)
	if cfg.Tick, err = duration(path, "daemon.tick", raw.Daemon.Tick, DefaultTick); err != nil {
		return Config{}, err
	}
	if cfg.Tick < minTick {
		return Config{}, fmt.Errorf("%s: daemon.tick is %s, which is too short; use \"1s\" or more", path, cfg.Tick)
	}
	if cfg.StallAfter, err = duration(path, "daemon.stall_after", raw.Daemon.StallAfter, DefaultStallAfter); err != nil {
		return Config{}, err
	}
	if raw.Daemon.MaxRestarts != nil {
		cfg.MaxRestarts = *raw.Daemon.MaxRestarts
	}
	if cfg.MaxRestarts < 0 {
		return Config{}, fmt.Errorf("%s: daemon.max_restarts is %d, which is negative; write 0 or more (0 means never restart a session)", path, cfg.MaxRestarts)
	}
	if cfg.RestartBackoff, err = duration(path, "daemon.restart_backoff", raw.Daemon.RestartBackoff, DefaultRestartBackoff); err != nil {
		return Config{}, err
	}
	if cfg.RestartBackoff <= 0 {
		return Config{}, fmt.Errorf("%s: daemon.restart_backoff is %s, which is not a wait above zero; write it like \"1m\"", path, cfg.RestartBackoff)
	}
	if raw.Daemon.MaxWakes != nil {
		cfg.MaxWakes = *raw.Daemon.MaxWakes
	}
	if cfg.MaxWakes < 0 {
		return Config{}, fmt.Errorf("%s: daemon.max_wakes is %d, which is negative; write 0 or more (0 means never wake a session)", path, cfg.MaxWakes)
	}

	if cfg.Governor, err = loadGovernor(path, raw); err != nil {
		return Config{}, err
	}

	if cfg.Web, err = loadWeb(path, raw); err != nil {
		return Config{}, err
	}

	var home string
	seen := map[string]bool{}
	for i, p := range raw.Project {
		key := fmt.Sprintf("project[%d].path", i)
		pp := strings.TrimSpace(p.Path)
		if pp == "" {
			return Config{}, fmt.Errorf("%s: %s is missing; give every [[project]] an absolute path", path, key)
		}
		if pp == "~" || strings.HasPrefix(pp, "~/") {
			if home == "" {
				if home, err = d.HomeDir(); err != nil || home == "" {
					return Config{}, fmt.Errorf("%s: %s uses ~ but the home directory is unknown; write the full path", path, key)
				}
			}
			pp = filepath.Join(home, strings.TrimPrefix(pp, "~"))
		}
		if !filepath.IsAbs(pp) {
			return Config{}, fmt.Errorf("%s: %s is %q, which is relative; write an absolute path or start it with ~/", path, key, p.Path)
		}
		pp = filepath.Clean(pp)
		name := strings.TrimSpace(p.Name)
		if name == "" {
			name = filepath.Base(pp)
		}
		if seen[name] {
			return Config{}, fmt.Errorf("%s: project[%d].name %q is used twice; give one of them a different name", path, i, name)
		}
		seen[name] = true
		cfg.Projects = append(cfg.Projects, Project{Name: name, Path: pp})
	}

	// Last, and never returned: see Config.DashErr. A bad [dash] value leaves
	// the defaults in place and everything above as it was read.
	if dash, derr := loadDash(path, raw, d); derr != nil {
		cfg.DashErr = derr
	} else {
		cfg.Dash = dash
	}
	return cfg, nil
}

// loadDash checks the [dash] table. Its error is for Config.DashErr only: Load
// must never return it (see DashErr). On an error the caller keeps the
// defaults for the whole table, not the keys that were fine, so a half-read
// table never runs a search the owner did not write.
func loadDash(path string, raw rawFile, d Deps) (Dash, error) {
	dash := defaultDash()
	if raw.Dash == nil {
		return dash, nil
	}
	table, ok := raw.Dash.(map[string]any)
	if !ok {
		return Dash{}, fmt.Errorf("%s: dash is not a table; write a [dash] line and put search_roots, scan_interval and max_depth under it", path)
	}

	if v, ok := table["scan_interval"]; ok {
		s, isString := v.(string)
		if !isString {
			return Dash{}, fmt.Errorf("%s: dash.scan_interval is %v, which is not text in quotes; write it like \"60s\"", path, v)
		}
		var err error
		if dash.ScanInterval, err = duration(path, "dash.scan_interval", s, DefaultScanInterval); err != nil {
			return Dash{}, err
		}
		if dash.ScanInterval < minScanInterval {
			return Dash{}, fmt.Errorf("%s: dash.scan_interval is %s, which is too short; use \"1s\" or more", path, dash.ScanInterval)
		}
	}

	if v, ok := table["max_depth"]; ok {
		n, isInt := v.(int64)
		if !isInt {
			return Dash{}, fmt.Errorf("%s: dash.max_depth is %#v, which is not a whole number; write it like 3, with no quotes", path, v)
		}
		// The second test catches a number too big for int on a 32-bit
		// build, where the conversion would wrap to something else.
		if n < 1 || int64(int(n)) != n {
			return Dash{}, fmt.Errorf("%s: dash.max_depth is %d; write a whole number of 1 or more, such as 3", path, n)
		}
		dash.MaxDepth = int(n)
	}

	if v, ok := table["search_roots"]; ok {
		list, isList := v.([]any)
		if !isList {
			return Dash{}, fmt.Errorf("%s: dash.search_roots is %#v, which is not a list; write it like [\"~/code\"]", path, v)
		}
		var home string
		for i, item := range list {
			root, isString := item.(string)
			if !isString {
				return Dash{}, fmt.Errorf("%s: dash.search_roots[%d] is %#v, which is not text in quotes; write each folder like \"~/code\"", path, i, item)
			}
			if root == "~" || strings.HasPrefix(root, "~/") {
				if home == "" {
					var err error
					if home, err = d.HomeDir(); err != nil || home == "" {
						return Dash{}, fmt.Errorf("%s: dash.search_roots[%d] uses ~ but the home directory is unknown; write the full path", path, i)
					}
				}
				root = filepath.Join(home, strings.TrimPrefix(root, "~"))
			}
			// A relative folder is allowed, unlike a [[project]] path: it is
			// taken from the folder the command was started in, which is what
			// the search settings have always meant.
			abs, err := filepath.Abs(root)
			if err != nil {
				return Dash{}, fmt.Errorf("%s: dash.search_roots[%d] is %q, which cannot be made into a full path (%v); write the full path", path, i, root, err)
			}
			dash.SearchRoots = append(dash.SearchRoots, abs)
		}
	}
	return dash, nil
}

func duration(path, key, val string, def time.Duration) (time.Duration, error) {
	if strings.TrimSpace(val) == "" {
		return def, nil
	}
	v, err := time.ParseDuration(strings.TrimSpace(val))
	if err != nil {
		return 0, fmt.Errorf("%s: %s is %q, which is not a duration; write it like \"5s\" or \"10m\"", path, key, val)
	}
	return v, nil
}

func defaultGovernor() Governor {
	th := governor.DefaultThresholds()
	return Governor{PauseAt: th.PauseAt, LaunchBelow: th.LaunchBelow, StaleAfter: th.StaleAfter,
		ProbeInterval: governor.DefaultProbeInterval, OnUnknownUsage: th.OnUnknownUsage}
}

// minProbeInterval keeps a typo such as "5s" from probing the provider's
// usage endpoint in a tight loop.
const minProbeInterval = time.Minute

// maxPauseAt is 100 percent. The floor of 5 exists because the weekly launch
// line is pause_at minus 5 (governor.weeklyMargin) and must stay above zero.
const (
	minPauseAt = 5
	maxPauseAt = 100
)

func loadGovernor(path string, raw rawFile) (Governor, error) {
	g := defaultGovernor()
	r := raw.Governor
	if r.PauseAt != nil {
		g.PauseAt = *r.PauseAt
	}
	if g.PauseAt <= minPauseAt || g.PauseAt > maxPauseAt {
		return Governor{}, fmt.Errorf("%s: governor.pause_at is %v; write a percent above %d and at most %d", path, g.PauseAt, minPauseAt, maxPauseAt)
	}
	if r.LaunchBelow != nil {
		g.LaunchBelow = *r.LaunchBelow
	}
	if g.LaunchBelow <= 0 || g.LaunchBelow > g.PauseAt {
		return Governor{}, fmt.Errorf("%s: governor.launch_below is %v; write a percent above 0 and at most governor.pause_at (%v)", path, g.LaunchBelow, g.PauseAt)
	}
	var err error
	if g.StaleAfter, err = duration(path, "governor.stale_after", r.StaleAfter, g.StaleAfter); err != nil {
		return Governor{}, err
	}
	if g.StaleAfter <= 0 {
		return Governor{}, fmt.Errorf("%s: governor.stale_after is %s, which is not an age above zero; write it like \"30m\"", path, g.StaleAfter)
	}
	if g.ProbeInterval, err = duration(path, "governor.probe_interval", r.ProbeInterval, g.ProbeInterval); err != nil {
		return Governor{}, err
	}
	if g.ProbeInterval < minProbeInterval {
		return Governor{}, fmt.Errorf("%s: governor.probe_interval is %s, which is too short; use \"1m\" or more", path, g.ProbeInterval)
	}
	if g.StaleAfter <= g.ProbeInterval {
		return Governor{}, fmt.Errorf("%s: governor.stale_after (%s) must be longer than governor.probe_interval (%s), or every reading goes stale between probes and new work is held for ever", path, g.StaleAfter, g.ProbeInterval)
	}
	if v := strings.TrimSpace(r.OnUnknownUsage); v != "" {
		g.OnUnknownUsage = v
	}
	if g.OnUnknownUsage != governor.UnknownRun && g.OnUnknownUsage != governor.UnknownHold {
		return Governor{}, fmt.Errorf("%s: governor.on_unknown_usage is %q; write %q or %q", path, g.OnUnknownUsage, governor.UnknownRun, governor.UnknownHold)
	}
	return g, nil
}

// loadWeb checks the [web] table. No error repeats the token: the message may
// end up in a terminal log or a screenshot.
func loadWeb(path string, raw rawFile) (Web, error) {
	r := raw.Web
	w := Web{Bind: DefaultWebBind, Token: r.Token}
	if b := strings.TrimSpace(r.Bind); b != "" {
		w.Bind = b
	}
	_, port, err := net.SplitHostPort(w.Bind)
	if err != nil {
		return Web{}, fmt.Errorf("%s: web.bind is %q, which is not host:port; write it like \"127.0.0.1:4253\"", path, w.Bind)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return Web{}, fmt.Errorf("%s: web.bind is %q, whose port is not a number from 1 to 65535; write it like \"127.0.0.1:4253\"", path, w.Bind)
	}
	if w.Token != "" {
		if strings.IndexFunc(w.Token, unicode.IsSpace) >= 0 {
			return Web{}, fmt.Errorf("%s: web.token contains white space; write a token of %d or more characters with no spaces", path, minTokenLen)
		}
		if len([]rune(w.Token)) < minTokenLen {
			return Web{}, fmt.Errorf("%s: web.token is too short; write a token of at least %d characters", path, minTokenLen)
		}
	}
	if w.Token == "" && !LoopbackBind(w.Bind) {
		return Web{}, fmt.Errorf("%s: web.bind is %q, which can be reached from other machines, and web.token is not set; set web.token to a secret of at least %d characters, or bind to 127.0.0.1", path, w.Bind, minTokenLen)
	}
	if pu := strings.TrimSpace(r.PublicURL); pu != "" {
		u, err := url.Parse(pu)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
			u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(pu, "#") {
			return Web{}, fmt.Errorf("%s: web.public_url is %q; write a full address such as \"https://afk.example\" that starts with http:// or https:// and has no ? or #", path, pu)
		}
		w.PublicURL = strings.TrimRight(pu, "/")
	}
	return w, nil
}
