package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// writeHostJSON seeds <worktree>/.afk/<name>/host.json with body.
func writeHostJSON(t *testing.T, worktree, name, body string) string {
	t.Helper()
	dir := filepath.Join(worktree, ".afk", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "host.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRuntimeFor pins how the runtime is chosen from host.json. Load-bearing
// rows: "no host.json" and "unparseable host.json" (a new session has none
// yet, and a corrupt one must stay cleanable, so both read as afk), "no
// runtime key" (every file written before the field existed), and the two
// unknown words (refused with ErrUnknownRuntime and a message that names the
// word and the session, so a session a newer afk started is never driven by
// an afk that does not know how).
func TestRuntimeFor(t *testing.T) {
	tests := []struct {
		name      string
		body      string // empty means no file
		wantAfk   bool
		wantInMsg []string
	}{
		{name: "no host.json", wantAfk: true},
		{name: "unparseable host.json", body: "{not json", wantAfk: true},
		{name: "no runtime key", body: `{"pid":1}`, wantAfk: true},
		{name: "runtime afk", body: `{"pid":1,"runtime":"afk"}`, wantAfk: true},
		{name: "runtime empty", body: `{"pid":1,"runtime":""}`, wantAfk: true},
		{name: "runtime rex", body: `{"pid":1,"runtime":"rex"}`, wantInMsg: []string{`"rex"`, "sess1"}},
		{name: "runtime bogus", body: `{"pid":1,"runtime":"bogus"}`, wantInMsg: []string{`"bogus"`, "sess1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wt := t.TempDir()
			if tt.body != "" {
				writeHostJSON(t, wt, "sess1", tt.body)
			}
			rt, err := RuntimeFor(wt, "sess1")
			if tt.wantAfk {
				if err != nil {
					t.Fatalf("RuntimeFor: %v", err)
				}
				if rt.Name() != "afk" {
					t.Errorf("runtime = %q, want afk", rt.Name())
				}
				return
			}
			if !errors.Is(err, ErrUnknownRuntime) {
				t.Fatalf("err = %v, want ErrUnknownRuntime", err)
			}
			if rt != nil {
				t.Errorf("runtime = %v, want nil on refusal", rt)
			}
			t.Logf("refusal: %v", err)
			for _, w := range tt.wantInMsg {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("message %q lacks %q", err.Error(), w)
				}
			}
		})
	}
}

// TestRuntimeCleanRefusesUnknownRuntime pins that clean with an unknown
// runtime signals nothing and removes nothing: host.json stays and no
// SignalKill or DialExit fake is called.
func TestRuntimeCleanRefusesUnknownRuntime(t *testing.T) {
	wt := t.TempDir()
	path := writeHostJSON(t, wt, "sess1", `{"pid":4242,"runtime":"bogus"}`)

	deps := testCleanDeps(t, t.TempDir())
	deps.Alive = func(int) bool { return true }
	deps.SignalKill = func(int, syscall.Signal) error {
		t.Fatal("SignalKill must not be called for an unknown runtime")
		return nil
	}
	deps.DialExit = func(string, time.Duration) error {
		t.Fatal("DialExit must not be called for an unknown runtime")
		return nil
	}

	err := terminateCleanHost(deps.withDefaults(), Session{Name: "sess1", WorktreePath: wt})
	if !errors.Is(err, ErrUnknownRuntime) {
		t.Fatalf("err = %v, want ErrUnknownRuntime", err)
	}
	t.Logf("refusal: %v", err)
	if !strings.Contains(err.Error(), "bogus") || !strings.Contains(err.Error(), "sess1") {
		t.Errorf("message %q should name the runtime and the session", err.Error())
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("host.json was removed: %v", statErr)
	}
}
