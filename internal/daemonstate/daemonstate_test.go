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

// Load-bearing rows: a damaged stop marker still stops the line (the safe
// reading of "something is there" is "stop"), and a second stop keeps the
// newest reason rather than the first.
func TestStop(t *testing.T) {
	t.Run("stop then read", func(t *testing.T) {
		p := t.TempDir()
		if Stopped(p) {
			t.Fatal("stopped before Stop")
		}
		if err := Stop(p, "T3", "deploy timed out", t0); err != nil {
			t.Fatal(err)
		}
		if !Stopped(p) {
			t.Fatal("not stopped after Stop")
		}
		b, _ := os.ReadFile(StopPath(p))
		if want := "{\"at\":\"2026-10-11T08:00:00Z\",\"task\":\"T3\",\"reason\":\"deploy timed out\"}\n"; string(b) != want {
			t.Fatalf("marker = %q, want %q", b, want)
		}
		task, reason, ok := ReadStop(p)
		if !ok || task != "T3" || reason != "deploy timed out" {
			t.Fatalf("ReadStop = %q, %q, %v", task, reason, ok)
		}
	})
	t.Run("second stop keeps newest reason", func(t *testing.T) {
		p := t.TempDir()
		if err := Stop(p, "T1", "first", t0); err != nil {
			t.Fatal(err)
		}
		if err := Stop(p, "T2", "second", t0.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		task, reason, ok := ReadStop(p)
		if !ok || task != "T2" || reason != "second" {
			t.Fatalf("ReadStop = %q, %q, %v", task, reason, ok)
		}
	})
	t.Run("missing is not stopped", func(t *testing.T) {
		p := t.TempDir()
		if Stopped(p) {
			t.Fatal("stopped for missing marker")
		}
		if task, reason, ok := ReadStop(p); ok || task != "" || reason != "" {
			t.Fatalf("ReadStop = %q, %q, %v", task, reason, ok)
		}
	})
	t.Run("damaged stop file still stopped", func(t *testing.T) {
		p := t.TempDir()
		if err := os.MkdirAll(filepath.Dir(StopPath(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(StopPath(p), []byte("not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		if !Stopped(p) {
			t.Fatal("damaged marker did not stop")
		}
		task, reason, ok := ReadStop(p)
		if !ok || task != "" || reason != "" {
			t.Fatalf("ReadStop = %q, %q, %v; want empty and ok", task, reason, ok)
		}
	})
}

// The "was it there" return is load-bearing: `afk resume` uses it to say
// "was not stopped" rather than claiming it cleared something. A damaged
// marker is removed too, because existence is the signal.
func TestClearStop(t *testing.T) {
	t.Run("clear when stopped", func(t *testing.T) {
		p := t.TempDir()
		if err := Stop(p, "T1", "r", t0); err != nil {
			t.Fatal(err)
		}
		was, err := ClearStop(p)
		if err != nil || !was {
			t.Fatalf("ClearStop = %v, %v", was, err)
		}
		if Stopped(p) {
			t.Fatal("still stopped after ClearStop")
		}
	})
	t.Run("clear when not stopped", func(t *testing.T) {
		was, err := ClearStop(t.TempDir())
		if err != nil || was {
			t.Fatalf("ClearStop = %v, %v", was, err)
		}
	})
	t.Run("clear removes damaged marker", func(t *testing.T) {
		p := t.TempDir()
		if err := os.MkdirAll(filepath.Dir(StopPath(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(StopPath(p), []byte("{torn"), 0o644); err != nil {
			t.Fatal(err)
		}
		was, err := ClearStop(p)
		if err != nil || !was {
			t.Fatalf("ClearStop = %v, %v", was, err)
		}
		if Stopped(p) {
			t.Fatal("damaged marker survived ClearStop")
		}
	})
}

// The zero-time rows are load-bearing: the daemon reads a zero LastShipPoll as
// "poll now", so a damaged or missing file must never look recent (that would
// stop the daemon asking GitHub at all).
func TestShipPoll(t *testing.T) {
	t.Run("missing file reads as zero", func(t *testing.T) {
		p := t.TempDir()
		if got := LastShipPoll(p); !got.IsZero() {
			t.Fatalf("LastShipPoll = %v, want zero", got)
		}
		if got := ShipPollFailingSince(p); !got.IsZero() {
			t.Fatalf("ShipPollFailingSince = %v, want zero", got)
		}
	})
	t.Run("mark then read", func(t *testing.T) {
		p := t.TempDir()
		if err := MarkShipPoll(p, t0); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(ShipPollPath(p))
		if want := "{\"at\":\"2026-10-11T08:00:00Z\",\"failing_since\":\"\"}\n"; string(b) != want {
			t.Fatalf("poll file = %q, want %q", b, want)
		}
		if got := LastShipPoll(p); !got.Equal(t0) {
			t.Fatalf("LastShipPoll = %v, want %v", got, t0)
		}
	})
	t.Run("damaged poll file reads as zero", func(t *testing.T) {
		p := t.TempDir()
		if err := os.MkdirAll(filepath.Dir(ShipPollPath(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(ShipPollPath(p), []byte("{torn"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := LastShipPoll(p); !got.IsZero() {
			t.Fatalf("LastShipPoll = %v, want zero", got)
		}
		if got := ShipPollFailingSince(p); !got.IsZero() {
			t.Fatalf("ShipPollFailingSince = %v, want zero", got)
		}
	})
	t.Run("mark replaces damaged file", func(t *testing.T) {
		p := t.TempDir()
		if err := os.MkdirAll(filepath.Dir(ShipPollPath(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(ShipPollPath(p), []byte("{torn"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := MarkShipPoll(p, t0); err != nil {
			t.Fatal(err)
		}
		if got := LastShipPoll(p); !got.Equal(t0) {
			t.Fatalf("LastShipPoll = %v, want %v", got, t0)
		}
	})
}

// Load-bearing rows: the outage start is kept across retries (moving it on
// every failed read would make a long outage look brand new), a failure still
// counts as an ask for the rate limit, and a good read does not end an outage
// by itself.
func TestShipPollFailure(t *testing.T) {
	t.Run("first failure sets failing since and poll time", func(t *testing.T) {
		p := t.TempDir()
		if err := MarkShipPollFailure(p, t0); err != nil {
			t.Fatal(err)
		}
		if got := ShipPollFailingSince(p); !got.Equal(t0) {
			t.Fatalf("ShipPollFailingSince = %v, want %v", got, t0)
		}
		if got := LastShipPoll(p); !got.Equal(t0) {
			t.Fatalf("LastShipPoll = %v, want %v", got, t0)
		}
	})
	t.Run("second failure keeps first failing since", func(t *testing.T) {
		p := t.TempDir()
		if err := MarkShipPollFailure(p, t0); err != nil {
			t.Fatal(err)
		}
		later := t0.Add(time.Hour)
		if err := MarkShipPollFailure(p, later); err != nil {
			t.Fatal(err)
		}
		if got := ShipPollFailingSince(p); !got.Equal(t0) {
			t.Fatalf("ShipPollFailingSince = %v, want %v", got, t0)
		}
		if got := LastShipPoll(p); !got.Equal(later) {
			t.Fatalf("LastShipPoll = %v, want %v", got, later)
		}
	})
	t.Run("good read keeps failing since until cleared", func(t *testing.T) {
		p := t.TempDir()
		if err := MarkShipPollFailure(p, t0); err != nil {
			t.Fatal(err)
		}
		if err := MarkShipPoll(p, t0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		if got := ShipPollFailingSince(p); !got.Equal(t0) {
			t.Fatalf("ShipPollFailingSince after good read = %v, want %v", got, t0)
		}
	})
	t.Run("clear empties failing since", func(t *testing.T) {
		p := t.TempDir()
		if err := MarkShipPollFailure(p, t0); err != nil {
			t.Fatal(err)
		}
		if err := ClearShipPollFailure(p); err != nil {
			t.Fatal(err)
		}
		if got := ShipPollFailingSince(p); !got.IsZero() {
			t.Fatalf("ShipPollFailingSince = %v, want zero", got)
		}
		if got := LastShipPoll(p); !got.Equal(t0) {
			t.Fatalf("LastShipPoll = %v, want %v (clear must not touch the poll time)", got, t0)
		}
	})
	t.Run("clear with nothing set writes no file", func(t *testing.T) {
		p := t.TempDir()
		if err := ClearShipPollFailure(p); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(ShipPollPath(p)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("poll file exists after clear with nothing set: %v", err)
		}
	})
	t.Run("failure on damaged file starts a new outage", func(t *testing.T) {
		p := t.TempDir()
		if err := os.MkdirAll(filepath.Dir(ShipPollPath(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(ShipPollPath(p), []byte("{torn"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := MarkShipPollFailure(p, t0); err != nil {
			t.Fatal(err)
		}
		if got := ShipPollFailingSince(p); !got.Equal(t0) {
			t.Fatalf("ShipPollFailingSince = %v, want %v", got, t0)
		}
	})
}
