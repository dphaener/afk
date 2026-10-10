package notify

import (
	"strings"
)

// AlerterArgs produces the full argv for alerter 26.x. Emits flags per W0.T1's
// verified behavior: --message, --title, --group, --sound default (alerter
// accepts the literal "default"), --subtitle (when non-empty), --actions Focus
// (when focusable=true), --app-icon (when iconPath is non-empty). NEVER emits
// --sender: W0.T1 confirmed that flag silently suppresses delivery entirely,
// even when pointed at the machine's only registered bundle id.
func AlerterArgs(t Toast, focusable bool, iconPath string) []string {
	args := []string{
		"--message", t.Body,
		"--title", t.Title,
		"--group", t.Group,
		"--sound", "default",
	}
	if t.Subtitle != "" {
		args = append(args, "--subtitle", t.Subtitle)
	}
	if focusable {
		args = append(args, "--actions", "Focus")
	}
	if iconPath != "" {
		args = append(args, "--app-icon", iconPath)
	}
	return args
}

// OsascriptArgs produces the full argv for osascript notification. Emits -e
// followed by ONE script string: display notification "<body>" with title
// "<title>", adding subtitle "<sub>" when non-empty and sound name "Ping".
// Escapes backslashes BEFORE double quotes (reversing that order double-escapes).
// Uses "Ping" not "default": there is no default.aiff in /System/Library/Sounds,
// and osascript with a bogus sound name exits 0 with no audio, so a wrong name
// is invisible to automated checks.
func OsascriptArgs(t Toast) []string {
	body := escapeAppleScript(t.Body)
	title := escapeAppleScript(t.Title)
	script := `display notification "` + body + `" with title "` + title + `"`
	if t.Subtitle != "" {
		subtitle := escapeAppleScript(t.Subtitle)
		script += ` subtitle "` + subtitle + `"`
	}
	script += ` sound name "Ping"`
	return []string{"-e", script}
}

// escapeAppleScript escapes backslashes BEFORE double quotes. The order matters:
// reversing it would double-escape backslashes that precede quotes. notify.sh:24-26
// in the repo escapes quotes only (a latent bug), which we must not reproduce.
func escapeAppleScript(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}
