package provider

import (
	"encoding/json"
	"errors"
	"path/filepath"
)

// ErrUnattendedUnsupported is returned by Unattended for a provider with no
// unattended permission mode afk knows. Callers must refuse to run that
// provider from the daemon rather than launch it and hope.
var ErrUnattendedUnsupported = errors.New("provider has no unattended mode afk knows")

// UnattendedDeps is everything UnattendedPreflight reads from the machine.
// All three are parameters so tests never touch the real home directory or
// the real user id.
type UnattendedDeps struct {
	// Home is the user's home directory.
	Home string
	// Euid returns the effective user id of the process.
	Euid func() int
	// ReadFile reads a file, normally os.ReadFile.
	ReadFile func(string) ([]byte, error)
}

// unattendedMode is the value of the agent CLI's --permission-mode option
// that skips permission prompts. The flag and the choice were read from
// `claude --help` (version 2.1.296), which lists `--permission-mode <mode>`
// with the choice "bypassPermissions". The older
// --dangerously-skip-permissions flag is deliberately not used.
const unattendedMode = "bypassPermissions"

// Unattended inserts `--permission-mode bypassPermissions` right after the
// program name. The flag goes first so it can never be swallowed as the
// value of a trailing option, and the input is copied, never mutated, because
// callers keep using the original argv for attended sessions.
func (claude) Unattended(argv []string) ([]string, error) {
	if len(argv) == 0 {
		return argv, errors.New("empty argv: nothing to make unattended")
	}
	out := make([]string, 0, len(argv)+2)
	out = append(out, argv[0], "--permission-mode", unattendedMode)
	out = append(out, argv[1:]...)
	return out, nil
}

func (codex) Unattended(argv []string) ([]string, error) {
	return nil, ErrUnattendedUnsupported
}

func (opencode) Unattended(argv []string) ([]string, error) {
	return nil, ErrUnattendedUnsupported
}

// UnattendedPreflight fails when the CLI would refuse or stop at a prompt
// for the unattended mode: the CLI refuses the mode as root, and it asks for
// a one-time confirmation unless ~/.claude/settings.json holds
// "skipDangerousModePermissionPrompt": true. The key name comes from this
// machine, where it is present and true after the warning was accepted; this
// code claims nothing more about the CLI's internals. A missing file, an
// unparsable file and a false or absent key all fail, since each leaves the
// prompt in place.
func (claude) UnattendedPreflight(d UnattendedDeps) (bool, string) {
	const steps = "Run the agent CLI once by hand with --permission-mode bypassPermissions and accept the warning it shows, so it is remembered. " +
		"Do not run the afk daemon as root: the CLI refuses this mode for root."
	if d.Euid != nil && d.Euid() == 0 {
		return false, "The afk daemon is running as root, and the agent CLI refuses its unattended mode for root. Run the daemon as your normal user. " + steps
	}
	if d.ReadFile == nil {
		return false, steps
	}
	data, err := d.ReadFile(claudeSettingsPath(d.Home))
	if err != nil {
		return false, "Could not read ~/.claude/settings.json, so the unattended-mode warning has not been accepted. " + steps
	}
	var settings struct {
		Skip bool `json:"skipDangerousModePermissionPrompt"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return false, "~/.claude/settings.json could not be read as JSON. Fix or remove it, then: " + steps
	}
	if !settings.Skip {
		return false, "The unattended-mode warning has not been accepted yet. " + steps
	}
	return true, ""
}

// claudeSettingsPath is where the agent CLI keeps the setting that remembers
// the unattended-mode warning. UnattendedPreflight and ReadUnattendedSetting
// both read it, so the path lives in one place.
func claudeSettingsPath(home string) string {
	return filepath.Join(home, ".claude", "settings.json")
}

const noUnattended = "This provider has no unattended mode afk knows, so afk cannot run it from the daemon. Use the default provider for daemon runs."

func (codex) UnattendedPreflight(UnattendedDeps) (bool, string) { return false, noUnattended }

func (opencode) UnattendedPreflight(UnattendedDeps) (bool, string) { return false, noUnattended }
