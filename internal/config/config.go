// Package config reads afk's user-level configuration file: the search
// roots, scan interval, and traversal depth that drive multi-project
// discovery (internal/scan). It is a leaf package — no internal/ imports —
// and only reads the file; writing or auto-creating it is out of scope here.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config is the user-level configuration for multi-project support.
type Config struct {
	SearchRoots  []string
	ScanInterval time.Duration
	MaxDepth     int
}

const (
	defaultScanInterval = 60 * time.Second
	defaultMaxDepth     = 3

	// minScanInterval is the floor Load enforces on an explicit scan_interval.
	// A scan walks every configured search root and shells out to `git
	// rev-parse` for every repo-root candidate it finds (internal/scan), so an
	// interval faster than the floor arms the next tick before a prior walk
	// over anything but a trivial tree can plausibly have finished, stacking
	// concurrent disk walks rather than replacing one with the next. 1s is
	// chosen because it is comfortably above single-digit-millisecond values
	// like "1ms" (the pathological case this guards against) while still well
	// below the 60s default, leaving room for a user who deliberately wants a
	// snappier switcher on a small set of roots.
	minScanInterval = 1 * time.Second
)

// Deps is the injectable seam for config's environment-touching operations.
// Real wiring comes from DefaultDeps; tests substitute fakes so Load and
// Path are hermetic and table-testable without a real home directory or
// filesystem.
type Deps struct {
	// Getenv looks up an environment variable, empty string if unset.
	Getenv func(string) string
	// HomeDir returns the user's home directory.
	HomeDir func() (string, error)
	// ReadFile reads a file's contents.
	ReadFile func(string) ([]byte, error)
}

// DefaultDeps returns Deps wired to the real environment.
func DefaultDeps() Deps {
	return Deps{
		Getenv:   os.Getenv,
		HomeDir:  os.UserHomeDir,
		ReadFile: os.ReadFile,
	}
}

// withDefaults fills in any nil field on d with its real-environment
// default.
func (d Deps) withDefaults() Deps {
	def := DefaultDeps()
	if d.Getenv == nil {
		d.Getenv = def.Getenv
	}
	if d.HomeDir == nil {
		d.HomeDir = def.HomeDir
	}
	if d.ReadFile == nil {
		d.ReadFile = def.ReadFile
	}
	return d
}

// rawConfig mirrors the on-disk JSON schema. MaxDepth is a pointer so an
// absent key (nil) is distinguishable from an explicit 0, letting Load fill
// only the fields the user actually omitted rather than zeroing the struct.
// An explicit 0 (or negative) is then rejected by Load as invalid rather than
// silently reinterpreted: internal/scan's Deps.MaxDepth is a plain int that
// treats any value below 1 as "the caller left this unset" (its zero value
// doubles as that sentinel for hermetic tests and zero-value Deps), so a real
// explicit 0 forwarded from here would be indistinguishable from an unset
// caller and get quietly replaced with scan.DefaultMaxDepth — a different
// number than the one the user wrote. Load refusing it here, at the one place
// this package's *int already tells the two cases apart, is what keeps that
// substitution from ever happening downstream.
type rawConfig struct {
	SearchRoots  []string `json:"search_roots"`
	ScanInterval string   `json:"scan_interval"`
	MaxDepth     *int     `json:"max_depth"`
}

// Path resolves the config file's location: $XDG_CONFIG_HOME/afk/config.json
// when XDG_CONFIG_HOME is set, else ~/.config/afk/config.json.
//
// This deliberately does not call os.UserConfigDir: on darwin that resolves
// to ~/Library/Application Support and ignores XDG_CONFIG_HOME entirely, so
// it would pick the wrong path on this project's primary platform. Checking
// XDG_CONFIG_HOME explicitly through the Getenv seam is the only way to get
// XDG-compliant behavior on darwin.
func Path(d Deps) (string, error) {
	d = d.withDefaults()
	if xdg := d.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "afk", "config.json"), nil
	}
	home, err := d.HomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, ".config", "afk", "config.json"), nil
}

// Load reads and parses afk's user-level config file. A missing file is
// the ordinary case — most users never create one — so it returns
// zero-value defaults and a nil error rather than an error; only malformed
// JSON, an out-of-range scan_interval or max_depth, or a file that exists but
// can't be read is an error.
func Load(d Deps) (Config, error) {
	d = d.withDefaults()

	cfg := Config{
		ScanInterval: defaultScanInterval,
		MaxDepth:     defaultMaxDepth,
	}

	path, err := Path(d)
	if err != nil {
		return Config{}, err
	}

	data, err := d.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return Config{}, fmt.Errorf("reading config %s: %w", path, err)
	}

	var raw rawConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return Config{}, fmt.Errorf("parsing config %s: %w", path, err)
	}

	if raw.ScanInterval != "" {
		dur, err := time.ParseDuration(raw.ScanInterval)
		if err != nil {
			return Config{}, fmt.Errorf("parsing scan_interval %q in %s: %w", raw.ScanInterval, path, err)
		}
		if dur < minScanInterval {
			return Config{}, fmt.Errorf("scan_interval %q in %s is below the %s floor (see minScanInterval)", raw.ScanInterval, path, minScanInterval)
		}
		cfg.ScanInterval = dur
	}

	if raw.MaxDepth != nil {
		if *raw.MaxDepth < 1 {
			return Config{}, fmt.Errorf("max_depth %d in %s must be at least 1 (see rawConfig's MaxDepth doc)", *raw.MaxDepth, path)
		}
		cfg.MaxDepth = *raw.MaxDepth
	}

	if len(raw.SearchRoots) > 0 {
		home, err := d.HomeDir()
		if err != nil {
			return Config{}, fmt.Errorf("resolving home directory: %w", err)
		}
		roots := make([]string, len(raw.SearchRoots))
		for i, root := range raw.SearchRoots {
			resolved, err := resolveRoot(root, home)
			if err != nil {
				return Config{}, err
			}
			roots[i] = resolved
		}
		cfg.SearchRoots = roots
	}

	return cfg, nil
}

// resolveRoot expands a leading "~/" (or bare "~") to home and makes the
// result absolute.
func resolveRoot(root, home string) (string, error) {
	if root == "~" || strings.HasPrefix(root, "~/") {
		root = filepath.Join(home, strings.TrimPrefix(root, "~"))
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolving search root %q: %w", root, err)
	}
	return abs, nil
}
