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

// writeMarker seeds <worktree>/.afk/<name>/runtime with body.
func writeMarker(t *testing.T, worktree, name, body string) {
	t.Helper()
	dir := filepath.Join(worktree, ".afk", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "runtime"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRuntimeFor pins how the runtime is chosen from host.json and, when
// host.json is absent, from the .afk/{name}/runtime marker. Load-bearing
// rows: "no host.json" and "unparseable host.json" (a new session has none
// yet, and a corrupt one must stay cleanable, so both read as afk, the
// second even beside a rex marker), "no runtime key" (every file written
// before the field existed), "no host.json, marker rex" (a Rex session whose
// host has stopped must be relaunched in Rex, T17.8 A7), "host.json beats
// the marker" (a live host's own word is the truth), and the unknown words
// (refused with ErrUnknownRuntime and a message that names the word and the
// session, so a session a newer afk started is never driven by an afk that
// does not know how).
func TestRuntimeFor(t *testing.T) {
	tests := []struct {
		name      string
		body      string // empty means no host.json
		marker    string // empty means no runtime marker
		want      string // runtime name; empty means refused
		wantInMsg []string
	}{
		{name: "no host.json", want: "afk"},
		{name: "unparseable host.json", body: "{not json", want: "afk"},
		{name: "unparseable host.json beside a rex marker", body: "{not json", marker: "rex\n", want: "afk"},
		{name: "no runtime key", body: `{"pid":1}`, want: "afk"},
		{name: "runtime afk", body: `{"pid":1,"runtime":"afk"}`, want: "afk"},
		{name: "runtime empty", body: `{"pid":1,"runtime":""}`, want: "afk"},
		{name: "runtime rex", body: `{"pid":1,"runtime":"rex"}`, want: "rex"},
		{name: "runtime bogus", body: `{"pid":1,"runtime":"bogus"}`, wantInMsg: []string{`"bogus"`, "sess1"}},
		{name: "no host.json, marker rex", marker: "rex\n", want: "rex"},
		{name: "no host.json, marker afk", marker: "afk\n", want: "afk"},
		{name: "no host.json, empty marker", marker: "\n", want: "afk"},
		{name: "no host.json, marker bogus", marker: "bogus\n", wantInMsg: []string{`"bogus"`, "sess1"}},
		{name: "host.json beats the marker", body: `{"pid":1,"runtime":"afk"}`, marker: "rex\n", want: "afk"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wt := t.TempDir()
			if tt.body != "" {
				writeHostJSON(t, wt, "sess1", tt.body)
			}
			if tt.marker != "" {
				writeMarker(t, wt, "sess1", tt.marker)
			}
			rt, err := RuntimeFor(wt, "sess1")
			if tt.want != "" {
				if err != nil {
					t.Fatalf("RuntimeFor: %v", err)
				}
				if rt.Name() != tt.want {
					t.Errorf("runtime = %q, want %s", rt.Name(), tt.want)
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

// TestRuntimeRex pins the rex runtime's identity and its Dial. Load-bearing
// row: "Dial with no socket" (the refusal comes before any dialling, so the
// words are the same whether or not the host is up). Spawn, Undo and Stop
// are TestRexRuntime's, in rexruntime_test.go. The last rows keep the afk
// words and the refusal of an unknown word unchanged.
func TestRuntimeRex(t *testing.T) {
	wt := t.TempDir()
	writeHostJSON(t, wt, "sess1", `{"pid":1,"runtime":"rex"}`)
	rt, err := RuntimeFor(wt, "sess1")
	if err != nil {
		t.Fatalf("RuntimeFor: %v", err)
	}
	if _, ok := rt.(rexRuntime); !ok || rt.Name() != "rex" {
		t.Fatalf("runtime = %T %q, want rexRuntime named rex", rt, rt.Name())
	}

	t.Run("Dial with no socket", func(t *testing.T) {
		conn, err := rt.Dial(t.TempDir(), "nosuch", time.Millisecond)
		if conn != nil {
			conn.Close()
			t.Fatal("Dial returned a connection")
		}
		if err == nil || err.Error() != "This session runs in Rex; open it there." {
			t.Errorf("err = %v, want the Rex sentence", err)
		}
	})
	for _, word := range []string{"", "afk"} {
		t.Run("word "+word, func(t *testing.T) {
			rt, err := runtimeNamed("s", word)
			if err != nil || rt.Name() != "afk" {
				t.Errorf("runtimeNamed(%q) = %v, %v; want afk", word, rt, err)
			}
		})
	}
	t.Run("unknown word", func(t *testing.T) {
		if _, err := runtimeNamed("s", "bogus"); !errors.Is(err, ErrUnknownRuntime) {
			t.Errorf("err = %v, want ErrUnknownRuntime", err)
		}
	})
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
