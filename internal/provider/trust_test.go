package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// trustFixture builds TrustDeps over a temp home with the real file
// operations and a Getenv fake.
func trustDeps(home string, env map[string]string) TrustDeps {
	d := DefaultTrustDeps(home)
	d.Getenv = func(k string) string { return env[k] }
	return d
}

func compact(t *testing.T, raw []byte) string {
	t.Helper()
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		t.Fatalf("compact: %v", err)
	}
	return b.String()
}

func readObj(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	return m
}

// TestTrustFolder load-bearing rows: "other values survive" (the file holds
// the owner's history, so nothing but the one key may change, including
// HTML characters and big numbers), "config dir" (the wrong file must never
// be touched), "broken json" and "missing file" and "projects not object"
// (a broken file may be the only copy, and a missing one must not be
// invented), "symlink" (the CLI keys by the resolved path), and the
// non-default provider rows (nothing may be read or written).
func TestTrustFolder(t *testing.T) {
	const base = `{"numStartups":12345678901234567890,"note":"a <b> & c","projects":{"/other":{"allowedTools":[],"x":"<&>"}}}`

	type tc struct {
		name     string
		provider string
		file     *string // content of $HOME/.claude.json; nil means missing
		mode     os.FileMode
		cfgDir   bool
		link     bool
		wantErr  error
		anyErr   bool
		check    func(t *testing.T, home, cfg, folder string)
	}
	s := func(v string) *string { return &v }

	cases := []tc{
		{name: "adds key for new folder", provider: "claude", file: s(base), check: func(t *testing.T, home, cfg, folder string) {
			m := readObj(t, filepath.Join(home, ".claude.json"))
			var projects map[string]json.RawMessage
			json.Unmarshal(m["projects"], &projects)
			if got := compact(t, projects[folder]); got != `{"hasTrustDialogAccepted":true}` {
				t.Fatalf("entry = %s", got)
			}
		}},
		{name: "other values survive", provider: "claude", file: s(base), check: func(t *testing.T, home, cfg, folder string) {
			m := readObj(t, filepath.Join(home, ".claude.json"))
			if got := compact(t, m["numStartups"]); got != `12345678901234567890` {
				t.Fatalf("number = %s", got)
			}
			if got := compact(t, m["note"]); got != `"a <b> & c"` {
				t.Fatalf("note = %s", got)
			}
			var projects map[string]json.RawMessage
			json.Unmarshal(m["projects"], &projects)
			if got := compact(t, projects["/other"]); got != `{"allowedTools":[],"x":"<&>"}` {
				t.Fatalf("other project = %s", got)
			}
		}},
		{name: "existing entry keeps fields", provider: "claude", file: s(`{"projects":{"FOLDER":{"history":[1,2],"hasTrustDialogAccepted":false}}}`), check: func(t *testing.T, home, cfg, folder string) {
			var projects map[string]json.RawMessage
			json.Unmarshal(readObj(t, filepath.Join(home, ".claude.json"))["projects"], &projects)
			if got := compact(t, projects[folder]); got != `{"hasTrustDialogAccepted":true,"history":[1,2]}` {
				t.Fatalf("entry = %s", got)
			}
		}},
		{name: "already true stays true", provider: "claude", file: s(`{"projects":{"FOLDER":{"hasTrustDialogAccepted":true}}}`), check: func(t *testing.T, home, cfg, folder string) {
			var projects map[string]json.RawMessage
			json.Unmarshal(readObj(t, filepath.Join(home, ".claude.json"))["projects"], &projects)
			if got := compact(t, projects[folder]); got != `{"hasTrustDialogAccepted":true}` {
				t.Fatalf("entry = %s", got)
			}
		}},
		{name: "no projects key", provider: "claude", file: s(`{"a":1}`), check: func(t *testing.T, home, cfg, folder string) {
			m := readObj(t, filepath.Join(home, ".claude.json"))
			if compact(t, m["a"]) != "1" || m["projects"] == nil {
				t.Fatal("projects not added or a lost")
			}
		}},
		{name: "config dir", provider: "claude", file: s(base), cfgDir: true, check: func(t *testing.T, home, cfg, folder string) {
			if got, _ := os.ReadFile(filepath.Join(home, ".claude.json")); string(got) != base {
				t.Fatal("home file was touched")
			}
			var projects map[string]json.RawMessage
			json.Unmarshal(readObj(t, filepath.Join(cfg, ".claude.json"))["projects"], &projects)
			if _, ok := projects[folder]; !ok {
				t.Fatal("config dir file not updated")
			}
		}},
		{name: "missing file", provider: "claude", file: nil, anyErr: true, check: func(t *testing.T, home, cfg, folder string) {
			if _, err := os.Stat(filepath.Join(home, ".claude.json")); !os.IsNotExist(err) {
				t.Fatal("file was created")
			}
		}},
		{name: "broken json", provider: "claude", file: s(`{"a":`), anyErr: true, check: func(t *testing.T, home, cfg, folder string) {
			if got, _ := os.ReadFile(filepath.Join(home, ".claude.json")); string(got) != `{"a":` {
				t.Fatal("file changed")
			}
		}},
		{name: "trailing data", provider: "claude", file: s(`{} {}`), anyErr: true, check: func(t *testing.T, home, cfg, folder string) {
			if got, _ := os.ReadFile(filepath.Join(home, ".claude.json")); string(got) != `{} {}` {
				t.Fatal("file changed")
			}
		}},
		{name: "projects not object", provider: "claude", file: s(`{"projects":[1]}`), anyErr: true, check: func(t *testing.T, home, cfg, folder string) {
			if got, _ := os.ReadFile(filepath.Join(home, ".claude.json")); string(got) != `{"projects":[1]}` {
				t.Fatal("file changed")
			}
		}},
		{name: "entry not object", provider: "claude", file: s(`{"projects":{"FOLDER":3}}`), anyErr: true},
		{name: "mode kept", provider: "claude", file: s(base), mode: 0o640, check: func(t *testing.T, home, cfg, folder string) {
			fi, err := os.Stat(filepath.Join(home, ".claude.json"))
			if err != nil || fi.Mode().Perm() != 0o640 {
				t.Fatalf("mode = %v, %v", fi.Mode().Perm(), err)
			}
		}},
		{name: "symlink keyed by resolved path", provider: "claude", file: s(base), link: true, check: func(t *testing.T, home, cfg, folder string) {
			var projects map[string]json.RawMessage
			json.Unmarshal(readObj(t, filepath.Join(home, ".claude.json"))["projects"], &projects)
			if _, ok := projects[folder]; !ok {
				t.Fatalf("resolved key %q missing", folder)
			}
			if len(projects) != 2 {
				t.Fatalf("projects = %d, want 2", len(projects))
			}
		}},
		{name: "codex unsupported", provider: "codex", file: s(base), wantErr: ErrTrustUnsupported},
		{name: "opencode unsupported", provider: "opencode", file: s(base), wantErr: ErrTrustUnsupported},
		{name: "unknown provider", provider: "nope", file: s(base), anyErr: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "home")
			cfg := filepath.Join(root, "cfg")
			for _, d := range []string{home, cfg} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			real := filepath.Join(root, "work", "proj")
			if err := os.MkdirAll(real, 0o755); err != nil {
				t.Fatal(err)
			}
			real, _ = filepath.EvalSymlinks(real)
			folder := real
			if c.link {
				folder = filepath.Join(root, "link")
				if err := os.Symlink(real, folder); err != nil {
					t.Fatal(err)
				}
			}
			target := filepath.Join(home, ".claude.json")
			env := map[string]string{}
			if c.cfgDir {
				env["CLAUDE_CONFIG_DIR"] = cfg
				target = filepath.Join(cfg, ".claude.json")
			}
			if c.file != nil {
				content := bytes.ReplaceAll([]byte(*c.file), []byte("FOLDER"), []byte(real))
				mode := c.mode
				if mode == 0 {
					mode = 0o600
				}
				// A config-dir row also seeds the home file so an
				// accidental write there is visible.
				for _, p := range []string{target, filepath.Join(home, ".claude.json")} {
					if err := os.WriteFile(p, content, mode); err != nil {
						t.Fatal(err)
					}
					os.Chmod(p, mode)
				}
			}
			err := TrustFolder(c.provider, folder, trustDeps(home, env))
			switch {
			case c.wantErr != nil:
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("err = %v, want %v", err, c.wantErr)
				}
			case c.anyErr:
				if err == nil {
					t.Fatal("want error")
				}
			case err != nil:
				t.Fatalf("err = %v", err)
			}
			if c.check != nil {
				c.check(t, home, cfg, real)
			}
		})
	}
}
