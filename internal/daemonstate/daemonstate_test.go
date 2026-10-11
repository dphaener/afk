package daemonstate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 11, 9, 0, 0, 0, time.FixedZone("x", 3600))

// Load-bearing rows: pausing twice keeps the first time, and a damaged flag
// still pauses (the safe reading of "something is there" is "stop").
func TestPause(t *testing.T) {
	t.Run("pause then resume", func(t *testing.T) {
		p := t.TempDir()
		if Paused(p) {
			t.Fatal("paused before Pause")
		}
		if err := Pause(p, t0); err != nil {
			t.Fatal(err)
		}
		if !Paused(p) {
			t.Fatal("not paused after Pause")
		}
		b, _ := os.ReadFile(PausePath(p))
		if want := "{\"at\":\"2026-10-11T08:00:00Z\"}\n"; string(b) != want {
			t.Fatalf("flag = %q, want %q", b, want)
		}
		was, err := Resume(p)
		if err != nil || !was {
			t.Fatalf("Resume = %v, %v", was, err)
		}
		if Paused(p) {
			t.Fatal("still paused after Resume")
		}
	})
	t.Run("pause twice keeps first time", func(t *testing.T) {
		p := t.TempDir()
		if err := Pause(p, t0); err != nil {
			t.Fatal(err)
		}
		first, _ := os.ReadFile(PausePath(p))
		if err := Pause(p, t0.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		second, _ := os.ReadFile(PausePath(p))
		if string(first) != string(second) {
			t.Fatalf("second Pause changed the flag: %q -> %q", first, second)
		}
	})
	t.Run("resume when not paused", func(t *testing.T) {
		was, err := Resume(t.TempDir())
		if err != nil || was {
			t.Fatalf("Resume = %v, %v", was, err)
		}
	})
	t.Run("damaged flag still pauses", func(t *testing.T) {
		p := t.TempDir()
		if err := os.MkdirAll(filepath.Dir(PausePath(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(PausePath(p), []byte("not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		if !Paused(p) {
			t.Fatal("damaged flag did not pause")
		}
		if err := Pause(p, t0); err != nil {
			t.Fatal(err)
		}
	})
}

func TestState(t *testing.T) {
	s := State{At: "2026-10-11T08:00:00Z", Kind: "launched", Task: "T1", Session: "s1", Reason: "r"}
	t.Run("round trip", func(t *testing.T) {
		p := t.TempDir()
		if err := WriteState(p, s); err != nil {
			t.Fatal(err)
		}
		got, ok := ReadState(p)
		if !ok || got != s {
			t.Fatalf("ReadState = %+v, %v", got, ok)
		}
	})
	t.Run("empty state keeps every key", func(t *testing.T) {
		p := t.TempDir()
		if err := WriteState(p, State{}); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(StatePath(p))
		if want := `{"at":"","kind":"","task":"","session":"","reason":""}` + "\n"; string(b) != want {
			t.Fatalf("file = %q", b)
		}
	})
	t.Run("missing is not present", func(t *testing.T) {
		if _, ok := ReadState(t.TempDir()); ok {
			t.Fatal("ok for missing file")
		}
	})
	t.Run("damaged is not present", func(t *testing.T) {
		p := t.TempDir()
		if err := WriteState(p, s); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(StatePath(p), []byte("{torn"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, ok := ReadState(p); ok {
			t.Fatal("ok for damaged file")
		}
	})
}

// The At-only row is load-bearing: it is why the daemon can skip a rewrite.
func TestSame(t *testing.T) {
	a := State{At: "1", Kind: "k", Task: "t", Session: "s", Reason: "r"}
	tests := []struct {
		name string
		b    State
		want bool
	}{
		{"identical", a, true},
		{"only At differs", State{At: "2", Kind: "k", Task: "t", Session: "s", Reason: "r"}, true},
		{"kind differs", State{At: "1", Kind: "x", Task: "t", Session: "s", Reason: "r"}, false},
		{"task differs", State{At: "1", Kind: "k", Task: "x", Session: "s", Reason: "r"}, false},
		{"session differs", State{At: "1", Kind: "k", Task: "t", Session: "x", Reason: "r"}, false},
		{"reason differs", State{At: "1", Kind: "k", Task: "t", Session: "s", Reason: "x"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := a.Same(tt.b); got != tt.want {
				t.Fatalf("Same = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLock(t *testing.T) {
	t.Run("lock records holder and creates folder", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "nested", "afk")
		unlock, err := Lock(dir, 4242, t0)
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		pid, at, ok := ReadMachine(dir)
		if !ok || pid != 4242 || at != "2026-10-11T08:00:00Z" {
			t.Fatalf("ReadMachine = %d, %q, %v", pid, at, ok)
		}
	})
	t.Run("second lock fails", func(t *testing.T) {
		dir := t.TempDir()
		unlock, err := Lock(dir, 1, t0)
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		if _, err := Lock(dir, 2, t0); !errors.Is(err, ErrAlreadyRunning) {
			t.Fatalf("second Lock err = %v, want ErrAlreadyRunning", err)
		}
		if pid, _, _ := ReadMachine(dir); pid != 1 {
			t.Fatalf("failed Lock overwrote running.json: pid %d", pid)
		}
	})
	t.Run("lock again after unlock", func(t *testing.T) {
		dir := t.TempDir()
		unlock, err := Lock(dir, 1, t0)
		if err != nil {
			t.Fatal(err)
		}
		unlock()
		unlock2, err := Lock(dir, 2, t0)
		if err != nil {
			t.Fatalf("Lock after unlock: %v", err)
		}
		unlock2()
	})
	t.Run("read machine missing or damaged", func(t *testing.T) {
		dir := t.TempDir()
		if _, _, ok := ReadMachine(dir); ok {
			t.Fatal("ok for missing")
		}
		if err := os.WriteFile(filepath.Join(dir, "running.json"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, ok := ReadMachine(dir); ok {
			t.Fatal("ok for damaged")
		}
	})
}
