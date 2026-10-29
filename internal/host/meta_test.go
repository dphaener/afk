package host

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMetaRoundTrip covers the liveness fields added on top of the base
// metadata: "both liveness fields set" and "wedged_since empty" are
// load-bearing for W1.T2 — they pin LastOutputAt/WedgedSince through a real
// WriteMeta+ReadMeta round trip so a broken writer or parser shows up here
// rather than only in the legacy-file test below.
func TestMetaRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		meta Meta
	}{
		{
			name: "typical",
			meta: Meta{PID: 12345, Socket: "/tmp/afk-501/foo-abcd1234.sock", StartedAt: "2026-07-28T00:00:00Z", Argv: []string{"afk", "attach", "foo"}},
		},
		{
			name: "empty argv",
			meta: Meta{PID: 1, Socket: "/tmp/x.sock", StartedAt: "2026-01-01T00:00:00Z", Argv: nil},
		},
		{
			name: "both liveness fields set",
			meta: Meta{PID: 2, Socket: "/tmp/y.sock", StartedAt: "2026-01-01T00:00:00Z", LastOutputAt: "2026-01-01T00:05:00Z", WedgedSince: "2026-01-01T00:06:00Z"},
		},
		{
			name: "wedged_since empty",
			meta: Meta{PID: 3, Socket: "/tmp/z.sock", StartedAt: "2026-01-01T00:00:00Z", LastOutputAt: "2026-01-01T00:05:00Z", WedgedSince: ""},
		},
		{
			name: "non-empty provider",
			meta: Meta{PID: 4, Socket: "/tmp/w.sock", StartedAt: "2026-01-01T00:00:00Z", Argv: []string{"codex"}, Provider: "codex"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repoRoot := t.TempDir()
			name := "sess-" + strings.ReplaceAll(tt.name, " ", "-")

			if err := WriteMeta(repoRoot, name, tt.meta); err != nil {
				t.Fatalf("WriteMeta: %v", err)
			}

			path := filepath.Join(repoRoot, ".afk", name, "host.json")
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("expected %s to exist: %v", path, err)
			}

			got, err := ReadMeta(repoRoot, name)
			if err != nil {
				t.Fatalf("ReadMeta: %v", err)
			}
			if got.PID != tt.meta.PID || got.Socket != tt.meta.Socket || got.StartedAt != tt.meta.StartedAt {
				t.Errorf("ReadMeta = %+v, want %+v", got, tt.meta)
			}
			if len(got.Argv) != len(tt.meta.Argv) {
				t.Errorf("Argv = %v, want %v", got.Argv, tt.meta.Argv)
			}
			if got.LastOutputAt != tt.meta.LastOutputAt {
				t.Errorf("LastOutputAt = %q, want %q", got.LastOutputAt, tt.meta.LastOutputAt)
			}
			if got.WedgedSince != tt.meta.WedgedSince {
				t.Errorf("WedgedSince = %q, want %q", got.WedgedSince, tt.meta.WedgedSince)
			}
			if got.Provider != tt.meta.Provider {
				t.Errorf("Provider = %q, want %q", got.Provider, tt.meta.Provider)
			}
		})
	}
}

// TestReadMetaMissingProvider pins Provider == "" for legacy host.json files:
// a non-empty value would relaunch old Claude sessions under the wrong binary.
func TestReadMetaMissingProvider(t *testing.T) {
	repoRoot := t.TempDir()
	name := "sess-no-provider"
	dir := filepath.Join(repoRoot, ".afk", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	legacy := `{
  "pid": 43,
  "socket": "/tmp/legacy-provider.sock",
  "started_at": "2026-01-01T00:00:00Z",
  "argv": ["claude", "/afk:go --session foo"]
}
`
	if err := os.WriteFile(filepath.Join(dir, "host.json"), []byte(legacy), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := ReadMeta(repoRoot, name)
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}
	if got.Provider != "" {
		t.Errorf("Provider = %q, want empty string for legacy file without the key", got.Provider)
	}
}

// TestReadMetaMissingLivenessFields pins the "missing field means unknown,
// never an error" contract: a host.json written by a host that predates
// W1.T2 has neither last_output_at nor wedged_since at all (not even as
// empty strings), and ReadMeta must still parse it cleanly with both fields
// reporting as the zero value.
func TestReadMetaMissingLivenessFields(t *testing.T) {
	repoRoot := t.TempDir()
	name := "sess-legacy"
	dir := filepath.Join(repoRoot, ".afk", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	legacy := `{
  "pid": 42,
  "socket": "/tmp/legacy.sock",
  "started_at": "2026-01-01T00:00:00Z",
  "argv": ["afk", "attach", "foo"]
}
`
	if err := os.WriteFile(filepath.Join(dir, "host.json"), []byte(legacy), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := ReadMeta(repoRoot, name)
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}
	if got.PID != 42 || got.Socket != "/tmp/legacy.sock" {
		t.Fatalf("ReadMeta = %+v, unexpected base fields", got)
	}
	if got.LastOutputAt != "" {
		t.Errorf("LastOutputAt = %q, want empty string for legacy file without the field", got.LastOutputAt)
	}
	if got.WedgedSince != "" {
		t.Errorf("WedgedSince = %q, want empty string for legacy file without the field", got.WedgedSince)
	}
}

func TestWriteMetaNoLeftoverTempFile(t *testing.T) {
	repoRoot := t.TempDir()
	if err := WriteMeta(repoRoot, "sess", Meta{PID: 1}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(repoRoot, ".afk", "sess"))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "host.json" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory entries = %v, want only host.json", names)
	}
}

func TestReadMetaMissing(t *testing.T) {
	repoRoot := t.TempDir()
	if _, err := ReadMeta(repoRoot, "does-not-exist"); err == nil {
		t.Error("ReadMeta: expected error for missing host.json, got nil")
	}
}

func TestAlive(t *testing.T) {
	tests := []struct {
		name string
		pid  int
		want bool
	}{
		{name: "self", pid: os.Getpid(), want: true},
		{name: "zero", pid: 0, want: false},
		{name: "negative", pid: -1, want: false},
		{name: "known-dead pid", pid: knownDeadPID(t), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Alive(tt.pid); got != tt.want {
				t.Errorf("Alive(%d) = %v, want %v", tt.pid, got, tt.want)
			}
		})
	}
}

func knownDeadPID(t *testing.T) int {
	t.Helper()
	cmd := "/bin/sh"
	if _, err := os.Stat(cmd); err != nil {
		t.Skip("/bin/sh not available to spawn a short-lived process")
	}
	proc, err := os.StartProcess(cmd, []string{cmd, "-c", "true"}, &os.ProcAttr{})
	if err != nil {
		t.Fatalf("StartProcess: %v", err)
	}
	state, err := proc.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !state.Exited() {
		t.Fatal("expected process to have exited")
	}
	return proc.Pid
}

func TestSocketPath(t *testing.T) {
	t.Run("basic derivation and prefix", func(t *testing.T) {
		path, err := SocketPath("/repo/one", "my-session")
		if err != nil {
			t.Fatalf("SocketPath: %v", err)
		}
		base := filepath.Base(path)
		if !strings.HasPrefix(base, "my-session-") {
			t.Errorf("socket filename %q does not start with name prefix %q", base, "my-session-")
		}
		if !strings.HasSuffix(base, ".sock") {
			t.Errorf("socket filename %q does not end in .sock", base)
		}
		info, err := os.Stat(filepath.Dir(path))
		if err != nil {
			t.Fatalf("expected socket parent dir to exist: %v", err)
		}
		if !info.IsDir() {
			t.Fatalf("parent %q is not a directory", filepath.Dir(path))
		}
	})

	t.Run("same name, different repo disambiguated by hash", func(t *testing.T) {
		p1, err := SocketPath("/repo/one", "shared-name")
		if err != nil {
			t.Fatalf("SocketPath: %v", err)
		}
		p2, err := SocketPath("/repo/two", "shared-name")
		if err != nil {
			t.Fatalf("SocketPath: %v", err)
		}
		if p1 == p2 {
			t.Errorf("expected different paths for different repoRoots, got %q for both", p1)
		}
	})

	t.Run("long name truncated to prefix", func(t *testing.T) {
		long := strings.Repeat("a", 64)
		path, err := SocketPath("/repo/one", long)
		if err != nil {
			t.Fatalf("SocketPath: %v", err)
		}
		base := filepath.Base(path)
		wantPrefix := long[:maxNamePrefixLen]
		if !strings.HasPrefix(base, wantPrefix) {
			t.Errorf("socket filename %q does not start with truncated prefix %q", base, wantPrefix)
		}
		if strings.HasPrefix(base, long) {
			t.Errorf("socket filename %q was not truncated", base)
		}
	})

	t.Run("length guard triggers on pathological TMPDIR", func(t *testing.T) {
		longTmp := t.TempDir()
		longTmp = filepath.Join(longTmp, strings.Repeat("x", 200))
		if err := os.MkdirAll(longTmp, 0o700); err != nil {
			t.Fatalf("setting up long TMPDIR: %v", err)
		}
		t.Setenv("TMPDIR", longTmp)

		_, err := SocketPath("/repo/one", "my-session")
		if err == nil {
			t.Fatal("SocketPath: expected length-guard error for pathological TMPDIR, got nil")
		}
	})
}

// TestMetaRex pins the rex block: "rex fields round trip" proves the ids are
// kept verbatim, "no rex key" is load-bearing because every host.json written
// before Rex must read with Rex == nil, and "not JSON" proves an unparseable
// file is still an error rather than an empty block.
func TestMetaRex(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		meta    *Meta
		wantRex *RexBlock
		wantErr bool
	}{
		{
			name: "rex fields round trip",
			meta: &Meta{PID: 3, Socket: "/tmp/r.sock", StartedAt: "2026-01-01T00:00:00Z", Runtime: RuntimeRex,
				Rex: &RexBlock{Server: "srv", SessionID: "s1", WindowID: "w1", BlockID: "b1"}},
			wantRex: &RexBlock{Server: "srv", SessionID: "s1", WindowID: "w1", BlockID: "b1"},
		},
		{
			name:    "optional ids omitted",
			meta:    &Meta{PID: 3, Runtime: RuntimeRex, Rex: &RexBlock{SessionID: "s1", BlockID: "b1"}},
			wantRex: &RexBlock{SessionID: "s1", BlockID: "b1"},
		},
		{
			name: "no rex key",
			raw:  `{"pid": 4, "socket": "/tmp/a.sock", "started_at": "2026-01-01T00:00:00Z", "argv": []}`,
		},
		{
			name:    "not JSON",
			raw:     "not json at all",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repoRoot := t.TempDir()
			const name = "sess"
			if tt.meta != nil {
				if err := WriteMeta(repoRoot, name, *tt.meta); err != nil {
					t.Fatalf("WriteMeta: %v", err)
				}
			} else {
				dir := filepath.Join(repoRoot, ".afk", name)
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "host.json"), []byte(tt.raw), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := ReadMeta(repoRoot, name)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ReadMeta error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			switch {
			case tt.wantRex == nil && got.Rex != nil:
				t.Errorf("Rex = %+v, want nil", got.Rex)
			case tt.wantRex != nil && (got.Rex == nil || *got.Rex != *tt.wantRex):
				t.Errorf("Rex = %+v, want %+v", got.Rex, tt.wantRex)
			}
		})
	}
}
