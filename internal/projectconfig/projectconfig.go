// Package projectconfig reads the project file, afk.toml, and the private
// afk.local.toml laid over it (docs/PLAN.md section 4.6).
//
// It understands three things: the top-level plan key (the master plan's
// path), the ntfy address under [notify], and the [ship] table that tells the
// daemon how to merge and deploy, including the optional after_merge command
// it runs in the main checkout after a merge (docs/PLAN.md T2.2a). Every other key and table ([policy], an
// unknown key inside [ship], anything unknown) is ignored on purpose: other
// parts of afk own those and read their own part, so this reader must never
// reject a file another part of afk understands. The plan reader and the inbox
// reader were written separately and are folded into this one loader so the
// file is read and parsed once.
//
// It reads two files under the root it is given. It does not read the machine
// config, the user's config directory or the environment.
package projectconfig

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// FileName is the project config file, at the project root.
const FileName = "afk.toml"

// LocalFileName is the private overlay beside FileName. It is git-ignored and
// holds values that must not be committed, such as the ntfy topic.
const LocalFileName = "afk.local.toml"

// DefaultPlan is the master plan path used when the file or the plan key is absent.
const DefaultPlan = "docs/PLAN.md"

// Config is the part of afk.toml this package understands.
type Config struct {
	// Plan is the master plan path, relative to the project root, cleaned.
	Plan string
	// Notify is the [notify] table.
	Notify Notify
	// Ship is the [ship] table, with defaults filled in for absent keys.
	Ship Ship
}

// Defaults for the [ship] table (docs/PLAN.md section 4.6). Base has none: the
// caller falls back to the session's own base branch.
const (
	DefaultMergeMethod   = "merge"
	DefaultCIReruns      = 1
	DefaultCIFixCycles   = 2
	DefaultDeploys       = true
	DefaultDeployTimeout = 15 * time.Minute
	// DefaultAfterMergeTimeout bounds the after-merge command. It is shorter
	// than the deploy timeout because the command is local work (an install, a
	// restart), not a wait on a remote deploy.
	DefaultAfterMergeTimeout = 10 * time.Minute
)

// Ship is the [ship] table: how the daemon merges a finished session and
// checks the deploy.
type Ship struct {
	// Base is the branch pull requests target. Empty means the session's base.
	Base string
	// MergeMethod is merge, squash or rebase.
	MergeMethod string
	// CIReruns is how many times a red CI is re-run before it goes back to the session.
	CIReruns int
	// CIFixCycles is how many times a red CI is handed back before the task parks.
	CIFixCycles int
	// Deploys says merging deploys, so the daemon checks health after the merge.
	Deploys bool
	// HealthURL is polled after the merge when Deploys is true. Empty means none.
	HealthURL string
	// DeployTimeout is how long the deploy may take to turn healthy.
	DeployTimeout time.Duration
	// RequiredEnvPattern is a regular expression; a diff that adds a match
	// parks the task before the merge. Empty means no check.
	RequiredEnvPattern string
	// AfterMerge is a shell command the daemon runs once in the project's main
	// checkout after a merge and the base-branch update, before the deploy
	// check (docs/PLAN.md T2.2a). A project that installs its own build output
	// needs this so a merge does not leave the old program running. It is
	// trimmed; empty means no command. It is never parsed or validated here:
	// it is the owner's shell text, and an error message must not repeat it.
	AfterMerge string
	// AfterMergeTimeout is how long AfterMerge may run before the daemon stops
	// it and stops the line.
	AfterMergeTimeout time.Duration
}

// defaultShip is the [ship] table when no file sets a key.
func defaultShip() Ship {
	return Ship{
		MergeMethod:   DefaultMergeMethod,
		CIReruns:      DefaultCIReruns,
		CIFixCycles:   DefaultCIFixCycles,
		Deploys:       DefaultDeploys,
		DeployTimeout: DefaultDeployTimeout,

		AfterMergeTimeout: DefaultAfterMergeTimeout,
	}
}

// EnvPattern returns the compiled RequiredEnvPattern, or nil when it is empty
// or does not compile. Load has already checked that it compiles, so nil from
// a loaded Config means only "no pattern".
func (s Ship) EnvPattern() *regexp.Regexp {
	if s.RequiredEnvPattern == "" {
		return nil
	}
	re, err := regexp.Compile(s.RequiredEnvPattern)
	if err != nil {
		return nil
	}
	return re
}

// Notify is the [notify] table.
type Notify struct {
	// NtfyURL is the ntfy address, trimmed. Empty means none is configured.
	NtfyURL string
}

// PlanPath returns the master plan's path under root.
func (c Config) PlanPath(root string) string {
	return filepath.Join(root, filepath.FromSlash(c.Plan))
}

// file mirrors the TOML layout. It declares only what Config carries; the
// decoder skips every other key without complaint. Fields are pointers or
// any so a key that is absent differs from one set to an empty value: the
// overlay needs that, because a local ntfy_url = "" must beat a committed
// address.
type file struct {
	// Plan is decoded as any so a wrong type is reported by us, with advice,
	// rather than as a decoder message. nil means the key is absent.
	Plan   any `toml:"plan"`
	Notify struct {
		NtfyURL *string `toml:"ntfy_url"`
	} `toml:"notify"`
	Ship struct {
		Base               *string `toml:"base"`
		MergeMethod        *string `toml:"merge_method"`
		CIReruns           *int    `toml:"ci_reruns"`
		CIFixCycles        *int    `toml:"ci_fix_cycles"`
		Deploys            *bool   `toml:"deploys"`
		HealthURL          *string `toml:"health_url"`
		DeployTimeout      *string `toml:"deploy_timeout"`
		RequiredEnvPattern *string `toml:"required_env_pattern"`
		AfterMerge         *string `toml:"after_merge"`
		AfterMergeTimeout  *string `toml:"after_merge_timeout"`
	} `toml:"ship"`
}

// Load reads {root}/afk.toml, then {root}/afk.local.toml, and lays the second
// over the first key by key (docs/PLAN.md section 4.6): a key present in the
// local file wins, even when empty; a key absent from it keeps the committed
// value. Either file may be missing. With both missing, or without a plan
// key in either, the plan is the default; a missing ntfy_url gives an empty
// address.
//
// The local file exists because the committed one is shared: a private value
// such as the ntfy topic belongs in the git-ignored afk.local.toml.
//
// Each file is checked on its own, so an error names the file the bad value
// came from. The plan path must stay inside the project. The daemon reads and
// acts on whatever file it names, so an absolute path or one climbing out with
// ".." would let a config point it at files outside the checkout.
func Load(root string) (Config, error) {
	base, err := readFile(filepath.Join(root, FileName))
	if err != nil {
		return Config{}, err
	}
	local, err := readFile(filepath.Join(root, LocalFileName))
	if err != nil {
		return Config{}, err
	}
	cfg := Config{Plan: DefaultPlan, Ship: defaultShip()}
	for _, f := range []*checked{base, local} {
		if f == nil {
			continue
		}
		if f.plan != nil {
			cfg.Plan = *f.plan
		}
		if f.ntfy != nil {
			cfg.Notify.NtfyURL = *f.ntfy
		}
		f.ship.apply(&cfg.Ship)
	}
	return cfg, nil
}

// checked holds one file's validated values; nil means the key was absent.
type checked struct {
	plan *string
	ntfy *string
	ship shipKeys
}

// shipKeys holds one file's validated [ship] values; nil means absent.
type shipKeys struct {
	base, mergeMethod, healthURL, envPattern *string
	afterMerge                               *string
	reruns, fixCycles                        *int
	deploys                                  *bool
	timeout, afterMergeTimeout               *time.Duration
}

// apply lays the keys this file set over s, so a later file wins key by key.
func (k shipKeys) apply(s *Ship) {
	if k.base != nil {
		s.Base = *k.base
	}
	if k.mergeMethod != nil {
		s.MergeMethod = *k.mergeMethod
	}
	if k.reruns != nil {
		s.CIReruns = *k.reruns
	}
	if k.fixCycles != nil {
		s.CIFixCycles = *k.fixCycles
	}
	if k.deploys != nil {
		s.Deploys = *k.deploys
	}
	if k.healthURL != nil {
		s.HealthURL = *k.healthURL
	}
	if k.timeout != nil {
		s.DeployTimeout = *k.timeout
	}
	if k.envPattern != nil {
		s.RequiredEnvPattern = *k.envPattern
	}
	if k.afterMerge != nil {
		s.AfterMerge = *k.afterMerge
	}
	if k.afterMergeTimeout != nil {
		s.AfterMergeTimeout = *k.afterMergeTimeout
	}
}

// checkShip validates one file's [ship] table. Errors name the file, never
// repeat health_url (it may carry a password) and say what to change.
func checkShip(name string, raw *file) (shipKeys, error) {
	r := raw.Ship
	var k shipKeys
	if r.Base != nil {
		b := strings.TrimSpace(*r.Base)
		k.base = &b
	}
	if r.MergeMethod != nil {
		m := strings.TrimSpace(*r.MergeMethod)
		if m != "merge" && m != "squash" && m != "rebase" {
			return k, fmt.Errorf("%s: [ship] merge_method %q is not one of merge, squash or rebase. Change it to one of those three words.", name, m)
		}
		k.mergeMethod = &m
	}
	for _, c := range []struct {
		key string
		in  *int
		out **int
	}{{"ci_reruns", r.CIReruns, &k.reruns}, {"ci_fix_cycles", r.CIFixCycles, &k.fixCycles}} {
		if c.in == nil {
			continue
		}
		if *c.in < 0 {
			return k, fmt.Errorf("%s: [ship] %s is %d, which is below zero. Set it to 0 or more.", name, c.key, *c.in)
		}
		v := *c.in
		*c.out = &v
	}
	k.deploys = r.Deploys
	if r.HealthURL != nil {
		h := strings.TrimSpace(*r.HealthURL)
		if h != "" {
			u, err := url.Parse(h)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return k, fmt.Errorf("%s: [ship] health_url must be empty or a web address that starts with http:// or https:// and names a server, for example https://example.com/health", name)
			}
		}
		k.healthURL = &h
	}
	if r.DeployTimeout != nil {
		d, err := time.ParseDuration(strings.TrimSpace(*r.DeployTimeout))
		if err != nil || d <= 0 {
			return k, fmt.Errorf("%s: [ship] deploy_timeout %q must be a length of time above zero, such as \"15m\" or \"1h30m\". Change it to one.", name, *r.DeployTimeout)
		}
		k.timeout = &d
	}
	if r.RequiredEnvPattern != nil {
		if _, err := regexp.Compile(*r.RequiredEnvPattern); err != nil {
			return k, fmt.Errorf("%s: [ship] required_env_pattern does not compile as a regular expression (%v). Fix the pattern, or set it to \"\" for none.", name, err)
		}
		k.envPattern = r.RequiredEnvPattern
	}
	if r.AfterMerge != nil {
		// Only trimmed. The text is shell the owner wrote, so it is not parsed,
		// and no error here quotes it.
		c := strings.TrimSpace(*r.AfterMerge)
		k.afterMerge = &c
	}
	if r.AfterMergeTimeout != nil {
		d, err := time.ParseDuration(strings.TrimSpace(*r.AfterMergeTimeout))
		if err != nil || d <= 0 {
			return k, fmt.Errorf("%s: [ship] after_merge_timeout %q must be a length of time above zero, such as \"10m\" or \"1h30m\". Change it to one.", name, *r.AfterMergeTimeout)
		}
		k.afterMergeTimeout = &d
	}
	return k, nil
}

// readFile reads and validates one config file. A missing file is nil, nil.
func readFile(path string) (*checked, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w. Check the file's permissions.", path, err)
	}

	var raw file
	if _, err := toml.Decode(string(data), &raw); err != nil {
		// The ntfy address may carry a password, so this message must not
		// repeat a value. The decoder's error is "toml: line N (last key
		// "k"): <message>" (ParseError.Error), and the type-mismatch message
		// names types, not values (MetaData.badtype). Its syntax messages
		// are not audited here; TestLoad's "broken toml" row checks only
		// that a value written next to a syntax error is not echoed.
		return nil, fmt.Errorf("%s is not valid TOML: %v. Fix the syntax and try again.", path, err)
	}

	var out checked
	if raw.Plan != nil {
		plan, err := checkPlan(path, raw.Plan)
		if err != nil {
			return nil, err
		}
		out.plan = &plan
	}
	if raw.Notify.NtfyURL != nil {
		ntfy := strings.TrimSpace(*raw.Notify.NtfyURL)
		if ntfy != "" {
			if err := checkURL(filepath.Base(path), ntfy); err != nil {
				return nil, err
			}
		}
		out.ntfy = &ntfy
	}
	ship, err := checkShip(filepath.Base(path), &raw)
	if err != nil {
		return nil, err
	}
	out.ship = ship
	return &out, nil
}

// checkPlan validates the decoded plan value and returns the cleaned path, or
// DefaultPlan when the key is absent.
func checkPlan(path string, v any) (string, error) {
	if v == nil {
		return DefaultPlan, nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s: plan must be a string such as %q, not %T. Change it to a quoted path.", path, DefaultPlan, v)
	}
	if strings.TrimSpace(s) == "" {
		return "", fmt.Errorf("%s: plan is empty. Set it to the master plan's path such as %q, or remove the line to use that default.", path, DefaultPlan)
	}
	if filepath.IsAbs(s) || strings.HasPrefix(s, "/") {
		return "", fmt.Errorf("%s: plan %q must be a relative path inside the project. Change it to a path relative to the project root.", path, s)
	}
	clean := filepath.Clean(filepath.FromSlash(s))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s: plan %q leaves the project. Change it to a path inside the project.", path, s)
	}
	if clean == "." {
		return "", fmt.Errorf("%s: plan %q is the project folder, not a file. Change it to the master plan file's path.", path, s)
	}
	return filepath.ToSlash(clean), nil
}

// checkURL requires an http or https address with a host. The error never
// repeats the address: url.Parse echoes its input, and the address may carry a
// password.
func checkURL(name, raw string) error {
	bad := name + ": [notify] ntfy_url must be a web address that starts with http:// or https:// and names a server, for example https://ntfy.sh/my-topic"
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New(bad)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New(bad)
	}
	return nil
}
