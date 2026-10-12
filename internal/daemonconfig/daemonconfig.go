// Package daemonconfig reads the build daemon's machine config:
// config.toml in afk's config folder.
//
// This is a second file beside the dashboard's config.json on purpose.
// docs/PLAN.md §2 chose TOML for settings a person edits by hand, and the
// JSON file belongs to the dashboard scanner, which rewrites it; sharing one
// file would let either side clobber the other. config.json is left alone.
//
// Folder rule: $XDG_CONFIG_HOME/afk when that variable is an absolute path,
// otherwise ~/.config/afk. That is the rule internal/usagestate uses and the
// one the XDG spec gives (a relative value must be ignored). internal/config
// uses the variable whenever it is non-empty, so under a relative
// XDG_CONFIG_HOME the two files can land in different folders. This reader
// follows the spec so that config.toml sits next to usage.json.
//
// Unknown keys are ignored: later tasks add governor and web keys to the same
// file, and an older daemon must not refuse a file a newer one wrote.
package daemonconfig

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
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
	Projects       []Project
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
	} `toml:"daemon"`
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
		MaxRestarts: DefaultMaxRestarts, RestartBackoff: DefaultRestartBackoff}
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
	return cfg, nil
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
