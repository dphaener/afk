package provider

import (
	"encoding/json"
	"errors"
	"io/fs"
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
