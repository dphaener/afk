// Package projectconfig reads the committed project file, afk.toml.
//
// This reader knows only what `afk inbox` needs: the ntfy address under
// [notify]. The full afk.toml loader belongs to master-plan task T1.1, and the
// two should be folded together once both are merged. Unknown keys and tables
// (plan, [ship], [policy]) are ignored on purpose, so one reader never rejects
// a file another part of afk understands.
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

// Config is the part of afk.toml that afk inbox reads.
type Config struct {
	Notify Notify
}

// Notify is the [notify] table.
type Notify struct {
	NtfyURL string
}

// file mirrors the TOML layout. It declares only what Config carries; the
// decoder skips every other key without complaint.
type file struct {
	Notify struct {
		NtfyURL string `toml:"ntfy_url"`
	} `toml:"notify"`
}

// Load reads {root}/afk.toml. A missing file is not an error: it returns a
// zero Config, because a project need not have one.
func Load(root string) (Config, error) {
	path := filepath.Join(root, "afk.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("could not read %s: %w", path, err)
	}
	var f file
	if _, err := toml.Decode(string(data), &f); err != nil {
		// The decoder's message quotes only the TOML text position and key
		// names, never a value, so it is safe to pass on.
		return Config{}, fmt.Errorf("afk.toml at %s could not be read as TOML: %w", path, err)
	}
	raw := strings.TrimSpace(f.Notify.NtfyURL)
	if raw != "" {
		if err := checkURL(raw); err != nil {
			return Config{}, err
		}
	}
	return Config{Notify: Notify{NtfyURL: raw}}, nil
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
