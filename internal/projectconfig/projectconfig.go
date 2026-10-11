// Package projectconfig reads the committed project file, afk.toml
// (docs/PLAN.md section 4.6).
//
// It understands two things: the top-level plan key (the master plan's path)
// and the ntfy address under [notify]. Every other key and table ([ship],
// [policy], anything unknown) is ignored on purpose: later tasks own those
// sections and each will read its own part, so this reader must never reject
// a file another part of afk understands. The plan reader and the inbox
// reader were written separately and are folded into this one loader so the
// file is read and parsed once.
//
// It reads one file under the root it is given. It does not read the machine
// config, the user's config directory or the environment.
package projectconfig

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// FileName is the project config file, at the project root.
const FileName = "afk.toml"

// DefaultPlan is the master plan path used when the file or the plan key is absent.
const DefaultPlan = "docs/PLAN.md"

// Config is the part of afk.toml this package understands.
type Config struct {
	// Plan is the master plan path, relative to the project root, cleaned.
	Plan string
	// Notify is the [notify] table.
	Notify Notify
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
// decoder skips every other key without complaint.
type file struct {
	// Plan is decoded as any so a wrong type is reported by us, with advice,
	// rather than as a decoder message.
	Plan   any `toml:"plan"`
	Notify struct {
		NtfyURL string `toml:"ntfy_url"`
	} `toml:"notify"`
}

// Load reads {root}/afk.toml. A missing file, or a file without a plan key,
// gives the default plan; a missing [notify] ntfy_url gives an empty address.
//
// The plan path must stay inside the project. The daemon reads and acts on
// whatever file it names, so an absolute path or one climbing out with ".."
// would let a committed config point it at files outside the checkout.
func Load(root string) (Config, error) {
	path := filepath.Join(root, FileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{Plan: DefaultPlan}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("cannot read %s: %w. Check the file's permissions.", path, err)
	}

	var raw file
	if _, err := toml.Decode(string(data), &raw); err != nil {
		// The ntfy address may carry a password, so this message must not
		// repeat a value. The decoder's error is "toml: line N (last key
		// "k"): <message>" (ParseError.Error), and the type-mismatch message
		// names types, not values (MetaData.badtype). Its syntax messages
		// are not audited here; TestLoad's "broken toml" row checks only
		// that a value written next to a syntax error is not echoed.
		return Config{}, fmt.Errorf("%s is not valid TOML: %v. Fix the syntax and try again.", path, err)
	}

	plan, err := checkPlan(path, raw.Plan)
	if err != nil {
		return Config{}, err
	}
	ntfy := strings.TrimSpace(raw.Notify.NtfyURL)
	if ntfy != "" {
		if err := checkURL(ntfy); err != nil {
			return Config{}, err
		}
	}
	return Config{Plan: plan, Notify: Notify{NtfyURL: ntfy}}, nil
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
func checkURL(raw string) error {
	const bad = "afk.toml: [notify] ntfy_url must be a web address that starts with http:// or https:// and names a server, for example https://ntfy.sh/my-topic"
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New(bad)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New(bad)
	}
	return nil
}
