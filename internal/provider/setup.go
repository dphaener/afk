package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// CLI returns the command name of the provider's agent CLI as typed on a
// shell (the empty name means Default). Each value is the same string the
// provider's own methods run, so a PATH lookup of it finds exactly the
// program afk would launch. Code outside this package asks here instead of
// spelling a provider's command, which keeps provider names in one package.
func CLI(name string) (string, error) {
	p, err := Lookup(name)
	if err != nil {
		return "", err
	}
	switch p.(type) {
	case claude:
		return "claude", nil
	case codex:
		return "codex", nil
	case opencode:
		return "opencode", nil
	}
	return "", errors.New("provider has no CLI name: " + p.Name())
}

// UnattendedSetting is the four-way state of a provider's unattended-mode
// setting. UnattendedPreflight returns one bool, which cannot tell "not set
// yet, afk will set it" from "the file is broken, afk must not overwrite
// it"; the install plan needs that difference.
type UnattendedSetting string

const (
	// UnattendedSet means the warning was accepted and is remembered.
	UnattendedSet UnattendedSetting = "set"
	// UnattendedNotSet means the settings file is missing or the key is
	// false or absent, so afk may write it.
	UnattendedNotSet UnattendedSetting = "not_set"
	// UnattendedBroken means the settings file cannot be read or is not a
	// JSON object; afk must leave it alone.
	UnattendedBroken UnattendedSetting = "broken"
	// UnattendedUnsupported means the provider has no unattended mode afk knows.
	UnattendedUnsupported UnattendedSetting = "unsupported"
)

// ReadUnattendedSetting reads the unattended-mode setting for the named
// provider. The second result is a plain-language reason for Broken and
// Unsupported and "" otherwise. Root is not considered here: the install
// plan handles root on its own. The file's content is never quoted in the
// reason, since a settings file can hold secrets.
func ReadUnattendedSetting(name string, d UnattendedDeps) (UnattendedSetting, string) {
	p, err := Lookup(name)
	if err != nil {
		return UnattendedBroken, err.Error()
	}
	if _, ok := p.(claude); !ok {
		return UnattendedUnsupported, noUnattended
	}
	if d.ReadFile == nil {
		return UnattendedBroken, "afk could not read ~/.claude/settings.json on this machine. Fix or remove it, then run afk install again."
	}
	data, err := d.ReadFile(claudeSettingsPath(d.Home))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return UnattendedNotSet, ""
		}
		return UnattendedBroken, "afk could not read ~/.claude/settings.json. Fix its permissions or remove it, then run afk install again."
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(data, &settings); err != nil || settings == nil {
		return UnattendedBroken, "~/.claude/settings.json is not a JSON object. Fix or remove it, then run afk install again."
	}
	var skip bool
	if raw, ok := settings["skipDangerousModePermissionPrompt"]; ok {
		if err := json.Unmarshal(raw, &skip); err != nil {
			skip = false
		}
	}
	if skip {
		return UnattendedSet, ""
	}
	return UnattendedNotSet, ""
}

// ErrSetupUnsupported means afk install has no setup command for the
// provider yet. Callers show its text to the owner as is.
var ErrSetupUnsupported = errors.New("afk install cannot set up that agent CLI yet. Install and sign in to it by hand, then run afk install again")

// InstallCommand is the argv the agent CLI's maker documents for installing
// it as a normal user. It is a shell pipeline because the maker's documented
// route is a download piped to bash; the install step hands the terminal to
// it so any prompt it shows reaches the owner.
func InstallCommand(name string) ([]string, error) {
	p, err := Lookup(name)
	if err != nil {
		return nil, err
	}
	if _, ok := p.(claude); !ok {
		return nil, ErrSetupUnsupported
	}
	return []string{"sh", "-c", "curl -fsSL https://claude.ai/install.sh | bash"}, nil
}

// LoginCommand is the argv of the agent CLI's own interactive login. afk
// never handles the credentials: the owner types them into the CLI itself.
func LoginCommand(name string) ([]string, error) {
	p, err := Lookup(name)
	if err != nil {
		return nil, err
	}
	if _, ok := p.(claude); !ok {
		return nil, ErrSetupUnsupported
	}
	return []string{"claude", "auth", "login"}, nil
}

// UnattendedWriteDeps is everything WriteUnattended touches on the machine,
// so tests write only under a temporary folder.
type UnattendedWriteDeps struct {
	// Home is the user's home directory.
	Home string
	// ReadFile reads a file, normally os.ReadFile.
	ReadFile func(string) ([]byte, error)
	// Stat reports a file's mode, normally os.Stat.
	Stat func(string) (fs.FileInfo, error)
	// MkdirAll makes a folder and its parents, normally os.MkdirAll.
	MkdirAll func(string, fs.FileMode) error
	// WriteTemp writes data to a new file in dir with the given mode and
	// returns its path.
	WriteTemp func(dir string, data []byte, mode fs.FileMode) (string, error)
	// Rename moves a file over another, normally os.Rename.
	Rename func(from, to string) error
	// Remove deletes a file, normally os.Remove; used to clean up a temporary
	// file when the rename fails.
	Remove func(string) error
}

// DefaultUnattendedWriteDeps returns the real-machine deps for home.
func DefaultUnattendedWriteDeps(home string) UnattendedWriteDeps {
	return UnattendedWriteDeps{
		Home:     home,
		ReadFile: os.ReadFile,
		Stat:     os.Stat,
		MkdirAll: os.MkdirAll,
		WriteTemp: func(dir string, data []byte, mode fs.FileMode) (string, error) {
			f, err := os.CreateTemp(dir, ".settings-*.tmp")
			if err != nil {
				return "", err
			}
			name := f.Name()
			if _, err := f.Write(data); err != nil {
				f.Close()
				os.Remove(name)
				return "", err
			}
			if err := f.Close(); err != nil {
				os.Remove(name)
				return "", err
			}
			if err := os.Chmod(name, mode); err != nil {
				os.Remove(name)
				return "", err
			}
			return name, nil
		},
		Rename: os.Rename,
		Remove: os.Remove,
	}
}

// WriteUnattended sets skipDangerousModePermissionPrompt to true in the agent
// CLI's settings file, keeping every other key. afk install writes it after
// its own safety screen so the owner never meets the agent's separate
// warning on a machine nobody is watching. A file that is not a JSON object
// is never overwritten: it may hold the owner's only copy of their settings.
// The write goes through a temporary file in the same folder and a rename, so
// a crash leaves the old file or the new one and never half of either.
func WriteUnattended(name string, d UnattendedWriteDeps) error {
	p, err := Lookup(name)
	if err != nil {
		return err
	}
	if _, ok := p.(claude); !ok {
		return ErrSetupUnsupported
	}
	if d.ReadFile == nil || d.Stat == nil || d.MkdirAll == nil || d.WriteTemp == nil || d.Rename == nil {
		return errors.New("afk could not write ~/.claude/settings.json on this machine")
	}
	path := claudeSettingsPath(d.Home)
	dir := filepath.Dir(path)
	const notObject = "~/.claude/settings.json is not a JSON object, so afk left it alone. Fix or remove it, then run afk install again."

	settings := map[string]json.RawMessage{}
	mode := fs.FileMode(0o600)
	data, err := d.ReadFile(path)
	switch {
	case err == nil:
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		var m map[string]json.RawMessage
		if err := dec.Decode(&m); err != nil || m == nil {
			return errors.New(notObject)
		}
		if dec.More() {
			return errors.New(notObject)
		}
		settings = m
		if fi, err := d.Stat(path); err == nil {
			mode = fi.Mode().Perm()
		}
	case errors.Is(err, fs.ErrNotExist):
	default:
		return errors.New("afk could not read ~/.claude/settings.json. Fix its permissions or remove it, then run afk install again.")
	}

	settings["skipDangerousModePermissionPrompt"] = json.RawMessage("true")
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return errors.New("afk could not encode ~/.claude/settings.json")
	}
	out = append(out, '\n')

	if err := d.MkdirAll(dir, 0o700); err != nil {
		return errors.New("afk could not make the ~/.claude folder. Check that your home folder is writable, then run afk install again.")
	}
	tmp, err := d.WriteTemp(dir, out, mode)
	if err != nil {
		return errors.New("afk could not write ~/.claude/settings.json. Check that the folder is writable, then run afk install again.")
	}
	if err := d.Rename(tmp, path); err != nil {
		if d.Remove != nil {
			d.Remove(tmp)
		}
		return errors.New("afk could not replace ~/.claude/settings.json. Check that the folder is writable, then run afk install again.")
	}
	return nil
}
