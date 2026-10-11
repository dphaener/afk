// Package projectconfig loads the committed per-project config file,
// afk.toml (docs/PLAN.md section 4.6). Only the plan key is read here.
package projectconfig

import (
	"errors"
	"fmt"
	"io/fs"
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
}

// PlanPath returns the master plan's path under root.
func (c Config) PlanPath(root string) string {
	return filepath.Join(root, filepath.FromSlash(c.Plan))
}

// Load reads {root}/afk.toml. A missing file, or a file without a plan key,
// gives the default plan.
//
// Every other key and table ([ship], [policy], [notify], anything unknown) is
// ignored on purpose: later tasks own those sections and each will read its
// own part, so this loader must not fail on a file that already contains them.
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

	// Plan is decoded as any so a wrong type is reported by us, with advice,
	// rather than as a decoder message.
	var raw struct {
		Plan any `toml:"plan"`
	}
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return Config{}, fmt.Errorf("%s is not valid TOML: %v. Fix the syntax and try again.", path, err)
	}
	if raw.Plan == nil {
		return Config{Plan: DefaultPlan}, nil
	}
	s, ok := raw.Plan.(string)
	if !ok {
		return Config{}, fmt.Errorf("%s: plan must be a string such as %q, not %T. Change it to a quoted path.", path, DefaultPlan, raw.Plan)
	}
	if strings.TrimSpace(s) == "" {
		return Config{}, fmt.Errorf("%s: plan is empty. Set it to the master plan's path such as %q, or remove the line to use that default.", path, DefaultPlan)
	}
	if filepath.IsAbs(s) || strings.HasPrefix(s, "/") {
		return Config{}, fmt.Errorf("%s: plan %q must be a relative path inside the project. Change it to a path relative to the project root.", path, s)
	}
	clean := filepath.Clean(filepath.FromSlash(s))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return Config{}, fmt.Errorf("%s: plan %q leaves the project. Change it to a path inside the project.", path, s)
	}
	if clean == "." {
		return Config{}, fmt.Errorf("%s: plan %q is the project folder, not a file. Change it to the master plan file's path.", path, s)
	}
	return Config{Plan: filepath.ToSlash(clean)}, nil
}
