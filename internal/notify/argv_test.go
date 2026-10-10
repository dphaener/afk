package notify

import (
	"reflect"
	"strings"
	"testing"
)

// TestAlerterArgs asserts exact slices for every flag decision. Load-bearing
// rows: subtitle present vs absent; focusable true vs false; iconPath empty
// (no --app-icon flag at all, load-bearing degradation) vs set; body with
// double quote (alerter carries it raw, argv needs no escaping); body with
// backslash; body with newline; --sender never appears (silently breaks delivery).
func TestAlerterArgs(t *testing.T) {
	tests := []struct {
		name      string
		toast     Toast
		focusable bool
		iconPath  string
		want      []string
	}{
		{
			name: "basic no subtitle no focus no icon",
			toast: Toast{
				Title: "afk · repo",
				Body:  "Needs input",
				Group: "afk-repo-session",
			},
			want: []string{
				"--message", "Needs input",
				"--title", "afk · repo",
				"--group", "afk-repo-session",
				"--sound", "default",
			},
		},
		{
			name: "subtitle present",
			toast: Toast{
				Title:    "afk · repo",
				Subtitle: "session-name",
				Body:     "Needs input",
				Group:    "afk-repo-session",
			},
			want: []string{
				"--message", "Needs input",
				"--title", "afk · repo",
				"--group", "afk-repo-session",
				"--sound", "default",
				"--subtitle", "session-name",
			},
		},
		{
			name: "focusable true",
			toast: Toast{
				Title: "afk · repo",
				Body:  "Click to focus",
				Group: "afk-repo-session",
			},
			focusable: true,
			want: []string{
				"--message", "Click to focus",
				"--title", "afk · repo",
				"--group", "afk-repo-session",
				"--sound", "default",
				"--actions", "Focus",
			},
		},
		{
			name: "iconPath set",
			toast: Toast{
				Title: "afk · repo",
				Body:  "With icon",
				Group: "afk-repo-session",
			},
			iconPath: "/path/to/icon.png",
			want: []string{
				"--message", "With icon",
				"--title", "afk · repo",
				"--group", "afk-repo-session",
				"--sound", "default",
				"--app-icon", "/path/to/icon.png",
			},
		},
		{
			name: "iconPath empty means no flag",
			toast: Toast{
				Title: "afk · repo",
				Body:  "No icon",
				Group: "afk-repo-session",
			},
			iconPath: "",
			want: []string{
				"--message", "No icon",
				"--title", "afk · repo",
				"--group", "afk-repo-session",
				"--sound", "default",
			},
		},
		{
			name: "body with double quote",
			toast: Toast{
				Title: "afk · repo",
				Body:  `Message with "quoted text" inside`,
				Group: "afk-repo-session",
			},
			want: []string{
				"--message", `Message with "quoted text" inside`,
				"--title", "afk · repo",
				"--group", "afk-repo-session",
				"--sound", "default",
			},
		},
		{
			name: "body with backslash",
			toast: Toast{
				Title: "afk · repo",
				Body:  `Path: C:\Windows\System32`,
				Group: "afk-repo-session",
			},
			want: []string{
				"--message", `Path: C:\Windows\System32`,
				"--title", "afk · repo",
				"--group", "afk-repo-session",
				"--sound", "default",
			},
		},
		{
			name: "body with newline",
			toast: Toast{
				Title: "afk · repo",
				Body:  "Line one\nLine two",
				Group: "afk-repo-session",
			},
			want: []string{
				"--message", "Line one\nLine two",
				"--title", "afk · repo",
				"--group", "afk-repo-session",
				"--sound", "default",
			},
		},
		{
			name: "all flags set",
			toast: Toast{
				Title:    "afk · repo",
				Subtitle: "session",
				Body:     "Full toast",
				Group:    "afk-repo-session",
			},
			focusable: true,
			iconPath:  "/icon.png",
			want: []string{
				"--message", "Full toast",
				"--title", "afk · repo",
				"--group", "afk-repo-session",
				"--sound", "default",
				"--subtitle", "session",
				"--actions", "Focus",
				"--app-icon", "/icon.png",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AlerterArgs(tt.toast, tt.focusable, tt.iconPath)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("AlerterArgs() =\n%v\nwant\n%v", got, tt.want)
			}
		})
	}
}

// TestAlerterArgsNoSender proves --sender is never emitted. This is load-bearing:
// W0.T1 confirmed that passing --sender silently suppresses delivery entirely,
// even when pointed at the machine's only registered bundle id.
func TestAlerterArgsNoSender(t *testing.T) {
	toast := Toast{
		Title:    "afk · repo",
		Subtitle: "session",
		Body:     "Message",
		Group:    "afk-repo-session",
	}
	args := AlerterArgs(toast, true, "/icon.png")
	for _, arg := range args {
		if arg == "--sender" {
			t.Fatalf("AlerterArgs() emitted --sender flag, which breaks delivery")
		}
	}
}

// TestOsascriptArgs asserts exact argv for the osascript backend. Load-bearing
// rows: subtitle present vs absent; body with double quote (must be escaped);
// body with backslash (escaped once, not twice); body with newline. Sound is
// "Ping" not "default" (no default.aiff exists, and a bogus name exits 0 silently).
func TestOsascriptArgs(t *testing.T) {
	tests := []struct {
		name  string
		toast Toast
		want  []string
	}{
		{
			name: "basic no subtitle",
			toast: Toast{
				Title: "afk · repo",
				Body:  "Needs input",
			},
			want: []string{
				"-e",
				`display notification "Needs input" with title "afk · repo" sound name "Ping"`,
			},
		},
		{
			name: "subtitle present",
			toast: Toast{
				Title:    "afk · repo",
				Subtitle: "session-name",
				Body:     "Needs input",
			},
			want: []string{
				"-e",
				`display notification "Needs input" with title "afk · repo" subtitle "session-name" sound name "Ping"`,
			},
		},
		{
			name: "body with double quote",
			toast: Toast{
				Title: "afk · repo",
				Body:  `Message with "quoted" text`,
			},
			want: []string{
				"-e",
				`display notification "Message with \"quoted\" text" with title "afk · repo" sound name "Ping"`,
			},
		},
		{
			name: "body with backslash",
			toast: Toast{
				Title: "afk · repo",
				Body:  `Path: C:\Windows`,
			},
			want: []string{
				"-e",
				`display notification "Path: C:\\Windows" with title "afk · repo" sound name "Ping"`,
			},
		},
		{
			name: "body with backslash before quote",
			toast: Toast{
				Title: "afk · repo",
				Body:  `Escape: \"quoted\"`,
			},
			want: []string{
				"-e",
				`display notification "Escape: \\\"quoted\\\"" with title "afk · repo" sound name "Ping"`,
			},
		},
		{
			name: "body with newline",
			toast: Toast{
				Title: "afk · repo",
				Body:  "Line one\nLine two",
			},
			want: []string{
				"-e",
				"display notification \"Line one\nLine two\" with title \"afk · repo\" sound name \"Ping\"",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := OsascriptArgs(tt.toast)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("OsascriptArgs() =\n%v\nwant\n%v", got, tt.want)
			}
		})
	}
}

// TestEscapeAppleScript pins the backslash-before-quote ordering. Escaping
// quotes before backslashes would double-escape any backslash that precedes
// a quote, which is wrong.
func TestEscapeAppleScript(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`plain`, `plain`},
		{`"quoted"`, `\"quoted\"`},
		{`back\slash`, `back\\slash`},
		{`\"combo\"`, `\\\"combo\\\"`},
		{`C:\Program Files\App`, `C:\\Program Files\\App`},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := escapeAppleScript(tt.input)
			if got != tt.want {
				t.Errorf("escapeAppleScript(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestOsascriptUsesCorrectSound confirms the sound is "Ping" not "default".
// /System/Library/Sounds has no default.aiff, and a bogus osascript sound name
// exits 0 silently (no audio, no error), so the wrong name is invisible to
// automated checks. This test asserts the right one.
func TestOsascriptUsesCorrectSound(t *testing.T) {
	toast := Toast{Title: "afk", Body: "Test"}
	got := OsascriptArgs(toast)
	if len(got) < 2 {
		t.Fatalf("OsascriptArgs() too short: %v", got)
	}
	script := got[1]
	if !strings.Contains(script, `sound name "Ping"`) {
		t.Errorf("OsascriptArgs() script does not contain 'sound name \"Ping\"': %s", script)
	}
	if strings.Contains(script, `sound name "default"`) {
		t.Errorf("OsascriptArgs() script contains 'sound name \"default\"', which is wrong for osascript: %s", script)
	}
}
