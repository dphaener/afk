package provider

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
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

// TestInstallCommand and TestLoginCommand: the unsupported rows are
// load-bearing (afk must not guess another CLI's install line).
func TestInstallCommand(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr error
	}{
		{"default", "", "curl -fsSL https://claude.ai/install.sh | bash", nil},
		{"claude", ClaudeName, "curl -fsSL https://claude.ai/install.sh | bash", nil},
		{"codex unsupported", CodexName, "", ErrSetupUnsupported},
		{"opencode unsupported", OpenCodeName, "", ErrSetupUnsupported},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := InstallCommand(tt.in)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == nil && (len(got) != 3 || got[0] != "sh" || got[1] != "-c" || got[2] != tt.want) {
				t.Fatalf("argv = %q", got)
			}
		})
	}
	if _, err := InstallCommand("nope"); err == nil {
		t.Fatal("unknown provider returned no error")
	}
}

func TestLoginCommand(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr error
	}{
		{"default", "", nil},
		{"claude", ClaudeName, nil},
		{"codex unsupported", CodexName, ErrSetupUnsupported},
		{"opencode unsupported", OpenCodeName, ErrSetupUnsupported},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := LoginCommand(tt.in)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == nil && strings.Join(got, " ") != "claude auth login" {
				t.Fatalf("argv = %q", got)
			}
		})
	}
}

// TestWriteUnattended: load-bearing rows are not_JSON and JSON_array (the
// file must be byte-identical afterwards, since it may hold the owner's only
// settings), other_keys (every key and number survives) and missing_file
// (created 0600 holding just the key). Each success is read back through
// ReadUnattendedSetting.
func TestWriteUnattended(t *testing.T) {
	tests := []struct {
		name      string
		existing  *string // nil: no file; with noDir, no folder either
		noDir     bool
		mode      fs.FileMode
		provider  string
		wantErr   error
		wantMsg   string
		wantExact string
		wantMode  fs.FileMode
	}{
		{name: "missing file", existing: nil, wantExact: "{\n  \"skipDangerousModePermissionPrompt\": true\n}\n", wantMode: 0o600},
		{name: "missing claude folder", noDir: true, wantExact: "{\n  \"skipDangerousModePermissionPrompt\": true\n}\n", wantMode: 0o600},
		{name: "other keys kept", existing: ptr(`{"theme":"dark","n":1.50,"big":12345678901234567890,"nested":{"a":[1,2]}}`),
			mode: 0o640, wantExact: "{\n  \"big\": 12345678901234567890,\n  \"n\": 1.50,\n  \"nested\": {\n    \"a\": [\n      1,\n      2\n    ]\n  },\n  \"skipDangerousModePermissionPrompt\": true,\n  \"theme\": \"dark\"\n}\n", wantMode: 0o640},
		{name: "key false becomes true", existing: ptr(`{"skipDangerousModePermissionPrompt": false}`), mode: 0o600,
			wantExact: "{\n  \"skipDangerousModePermissionPrompt\": true\n}\n", wantMode: 0o600},
		{name: "empty object", existing: ptr(`{}`), mode: 0o600,
			wantExact: "{\n  \"skipDangerousModePermissionPrompt\": true\n}\n", wantMode: 0o600},
		{name: "not JSON", existing: ptr("not json {"), mode: 0o600, wantMsg: "not a JSON object"},
		{name: "JSON array", existing: ptr(`[1,2]`), mode: 0o600, wantMsg: "not a JSON object"},
		{name: "JSON null", existing: ptr(`null`), mode: 0o600, wantMsg: "not a JSON object"},
		{name: "trailing junk", existing: ptr(`{} x`), mode: 0o600, wantMsg: "not a JSON object"},
		{name: "codex unsupported", provider: CodexName, wantErr: ErrSetupUnsupported},
		{name: "opencode unsupported", provider: OpenCodeName, wantErr: ErrSetupUnsupported},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			dir := filepath.Join(home, ".claude")
			path := filepath.Join(dir, "settings.json")
			if !tt.noDir && (tt.existing != nil || tt.wantErr == nil && tt.wantMsg == "") {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if tt.noDir {
				// leave the folder absent
			} else if tt.existing != nil {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(*tt.existing), tt.mode); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, tt.mode); err != nil {
					t.Fatal(err)
				}
			}
			err := WriteUnattended(tt.provider, DefaultUnattendedWriteDeps(home))
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if tt.wantMsg != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantMsg) || !strings.Contains(err.Error(), "settings.json") {
					t.Fatalf("err = %v, want it to mention %q and the file", err, tt.wantMsg)
				}
				got, rerr := os.ReadFile(path)
				if rerr != nil || string(got) != *tt.existing {
					t.Fatalf("file changed: %q, %v", got, rerr)
				}
				assertNoTemp(t, dir)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != tt.wantExact {
				t.Fatalf("file = %q, %v; want %q", got, err, tt.wantExact)
			}
			fi, err := os.Stat(path)
			if err != nil || fi.Mode().Perm() != tt.wantMode {
				t.Fatalf("mode = %v, %v; want %v", fi.Mode().Perm(), err, tt.wantMode)
			}
			if tt.noDir {
				di, err := os.Stat(dir)
				if err != nil || di.Mode().Perm() != 0o700 {
					t.Fatalf("folder mode = %v, %v", di, err)
				}
			}
			assertNoTemp(t, dir)
			state, reason := ReadUnattendedSetting("", UnattendedDeps{Home: home, ReadFile: os.ReadFile})
			if state != UnattendedSet {
				t.Fatalf("read back %q (%s), want set", state, reason)
			}
		})
	}
}

// TestWriteUnattendedRenameFails: a failed rename leaves the old file as it
// was and no temporary file behind.
func TestWriteUnattendedRenameFails(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude")
	path := filepath.Join(dir, "settings.json")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"a":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	d := DefaultUnattendedWriteDeps(home)
	d.Rename = func(string, string) error { return errors.New("boom") }
	if err := WriteUnattended("", d); err == nil {
		t.Fatal("want an error")
	}
	got, _ := os.ReadFile(path)
	if string(got) != `{"a":1}` {
		t.Fatalf("file changed: %q", got)
	}
	assertNoTemp(t, dir)
}

func ptr(s string) *string { return &s }

func assertNoTemp(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Name() != "settings.json" {
			t.Errorf("stray file %q left in %s", e.Name(), dir)
		}
	}
}
