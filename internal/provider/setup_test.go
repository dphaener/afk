package provider

import (
	"io/fs"
	"strings"
	"testing"
)

// TestCLI: the unknown-name row is load-bearing (an error, never a guessed
// command); the empty-name row proves the default is used.
func TestCLI(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"default", "", "claude", false},
		{"claude", ClaudeName, "claude", false},
		{"codex", CodexName, "codex", false},
		{"opencode", OpenCodeName, "opencode", false},
		{"unknown", "nope", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CLI(tt.in)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("CLI(%q) = %q, %v; want %q, err=%v", tt.in, got, err, tt.want, tt.wantErr)
			}
		})
	}
}

// TestReadUnattendedSetting: load-bearing rows are not_JSON, JSON_array and
// unreadable (a broken file must never read as "not set", or afk would
// overwrite it) and key_true (the only Set).
func TestReadUnattendedSetting(t *testing.T) {
	file := func(s string) func(string) ([]byte, error) {
		return func(p string) ([]byte, error) {
			if p != "/h/.claude/settings.json" {
				t.Errorf("read %q", p)
			}
			return []byte(s), nil
		}
	}
	fail := func(err error) func(string) ([]byte, error) {
		return func(string) ([]byte, error) { return nil, err }
	}
	tests := []struct {
		name       string
		provider   string
		read       func(string) ([]byte, error)
		want       UnattendedSetting
		wantReason string // substring; "" means the reason must be empty
	}{
		{"missing file", "", fail(fs.ErrNotExist), UnattendedNotSet, ""},
		{"unreadable", "", fail(fs.ErrPermission), UnattendedBroken, "~/.claude/settings.json"},
		{"not JSON", "", file("{oops"), UnattendedBroken, "Fix or remove"},
		{"JSON array", "", file("[]"), UnattendedBroken, "not a JSON object"},
		{"JSON null", "", file("null"), UnattendedBroken, "not a JSON object"},
		{"key false", "", file(`{"skipDangerousModePermissionPrompt":false}`), UnattendedNotSet, ""},
		{"key absent", "", file(`{"other":1}`), UnattendedNotSet, ""},
		{"key true", "", file(`{"skipDangerousModePermissionPrompt":true}`), UnattendedSet, ""},
		{"unsupported provider", CodexName, file("{}"), UnattendedUnsupported, "no unattended mode"},
		{"unknown provider", "nope", file("{}"), UnattendedBroken, "nope"},
		{"nil ReadFile", "", nil, UnattendedBroken, "~/.claude/settings.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reason := ReadUnattendedSetting(tt.provider, UnattendedDeps{Home: "/h", ReadFile: tt.read})
			if got != tt.want {
				t.Fatalf("state = %q, want %q (reason %q)", got, tt.want, reason)
			}
			if tt.wantReason == "" && reason != "" {
				t.Fatalf("reason = %q, want empty", reason)
			}
			if !strings.Contains(reason, tt.wantReason) {
				t.Fatalf("reason = %q, want it to contain %q", reason, tt.wantReason)
			}
		})
	}
}
