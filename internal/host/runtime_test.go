package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestMetaRuntime is a table over how the runtime field reads and writes. The
// load-bearing rows are "no key reads empty" (every host.json written before
// the field must keep reading as afk) and "bare Meta omits the key" (dropping
// omitempty would stamp an empty runtime into files other writers produce).
func TestMetaRuntime(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"file without the key reads empty", func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, ".afk", "s")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			legacy := `{"pid":1,"socket":"/x","started_at":"2026-01-01T00:00:00Z","argv":["a"]}`
			if err := os.WriteFile(filepath.Join(dir, "host.json"), []byte(legacy), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := ReadMeta(root, "s")
			if err != nil {
				t.Fatalf("ReadMeta: %v", err)
			}
			if got.Runtime != "" {
				t.Errorf("Runtime = %q, want empty", got.Runtime)
			}
		}},
		{"round trip keeps afk", func(t *testing.T) {
			root := t.TempDir()
			if err := WriteMeta(root, "s", Meta{PID: 1, Runtime: RuntimeAfk}); err != nil {
				t.Fatal(err)
			}
			got, err := ReadMeta(root, "s")
			if err != nil {
				t.Fatal(err)
			}
			if got.Runtime != "afk" {
				t.Errorf("Runtime = %q, want afk", got.Runtime)
			}
		}},
		{"bare Meta marshals without a runtime key", func(t *testing.T) {
			data, err := json.Marshal(Meta{})
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatal(err)
			}
			if _, ok := m["runtime"]; ok {
				t.Errorf("marshaled %s has a runtime key, want it omitted", data)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, tc.run)
	}
}

// TestServeWritesRuntimeAfk is the end-to-end row: a real host writes
// "runtime": "afk" into host.json, and it is still there after a later
// rewrite, because every rewrite starts from baseMeta. The child prints one
// report, waits for a gate, then prints a second, so the test sees the file
// before and after a publish that replaced the agent status. It waits for the
// file rather than reading once, because Serve listens before it writes it.
func TestServeWritesRuntimeAfk(t *testing.T) {
	requireUnixBind(t)
	gate := filepath.Join(t.TempDir(), "gate")
	h := startServe(t, agentReportScript(gate))

	awaitMeta(t, h.cfg, "carried runtime afk with the working report", func(m Meta) bool {
		return m.Runtime == RuntimeAfk && m.AgentStatus != nil && m.AgentStatus.State == "working"
	})
	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	awaitMeta(t, h.cfg, "kept runtime afk after the idle report rewrote it", func(m Meta) bool {
		return m.AgentStatus != nil && m.AgentStatus.State == "idle" && m.Runtime == RuntimeAfk
	})

	data, err := os.ReadFile(h.cfg.MetaPath)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["runtime"] != "afk" {
		t.Errorf("host.json runtime = %v, want \"afk\"; file: %s", raw["runtime"], data)
	}
}
