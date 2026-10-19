package session

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/host"
)

// TestStopHost pins the host-only stop that approving a plan relies on.
// Load-bearing rows: "dead pid" and "dies after exit frame" (the files go but
// the sibling planning marker stays, and no signal is sent when the Exit
// frame is enough), and "never dies" and "signal fails while alive" (an error
// naming the pid, with the planning marker and host.json still in place).
// Timings are shrunk, so no row waits on the real ladder.
func TestStopHost(t *testing.T) {
	tests := []struct {
		name        string
		seed        bool // write host.json, socket file and host.log
		corrupt     bool // write an unparseable host.json instead
		alive       func(calls *int) bool
		signalErr   error
		want        HostStop
		wantErr     bool
		wantSignals int
		wantFiles   bool // host.json/host.log/socket still present after
	}{
		{
			name: "no host.json",
			want: HostGone,
		},
		{
			name:    "corrupt host.json",
			corrupt: true,
			want:    HostGone,
		},
		{
			name:  "dead pid",
			seed:  true,
			alive: func(*int) bool { return false },
			want:  HostGone,
		},
		{
			name: "dies after exit frame",
			seed: true,
			alive: func(c *int) bool {
				*c++
				return *c <= 2
			},
			want: HostStopped,
		},
		{
			name:        "never dies",
			seed:        true,
			alive:       func(*int) bool { return true },
			wantErr:     true,
			wantSignals: 2,
			wantFiles:   true,
		},
		{
			name:        "signal fails while alive",
			seed:        true,
			alive:       func(*int) bool { return true },
			signalErr:   syscall.EPERM,
			wantErr:     true,
			wantSignals: 1,
			wantFiles:   true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			shrinkHostTimings(t)
			wt := t.TempDir()
			dir := filepath.Join(wt, ".afk", "master-plan")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			planning := filepath.Join(dir, "planning")
			if err := os.WriteFile(planning, []byte("1\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			meta := filepath.Join(dir, "host.json")
			logf := filepath.Join(dir, "host.log")
			sock := filepath.Join(dir, "host.sock")
			if tc.seed {
				if err := host.WriteMeta(wt, "master-plan", host.Meta{PID: 4242, Socket: sock}); err != nil {
					t.Fatal(err)
				}
				for _, p := range []string{logf, sock} {
					if err := os.WriteFile(p, nil, 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}
			if tc.corrupt {
				if err := os.WriteFile(meta, []byte("{not json"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(logf, nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}

			calls := 0
			signals := 0
			d := CleanDeps{
				Stderr:   &bytes.Buffer{},
				DialExit: func(string, time.Duration) error { return nil },
				SignalKill: func(int, syscall.Signal) error {
					signals++
					return tc.signalErr
				},
			}
			if tc.alive != nil {
				d.Alive = func(int) bool { return tc.alive(&calls) }
			}

			got, err := StopHost(wt, "master-plan", d)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr && !strings.Contains(err.Error(), "pid 4242") {
				t.Errorf("err = %q, want it to name the pid", err)
			}
			if !tc.wantErr && got != tc.want {
				t.Errorf("result = %v, want %v", got, tc.want)
			}
			if signals != tc.wantSignals {
				t.Errorf("signals = %d, want %d", signals, tc.wantSignals)
			}
			if _, err := os.Stat(planning); err != nil {
				t.Errorf("planning marker gone: %v", err)
			}
			if tc.seed {
				for _, p := range []string{meta, logf, sock} {
					_, err := os.Stat(p)
					if present := err == nil; present != tc.wantFiles {
						t.Errorf("%s present = %v, want %v", filepath.Base(p), present, tc.wantFiles)
					}
				}
			}
			if tc.corrupt {
				for _, p := range []string{meta, logf} {
					if _, err := os.Stat(p); err == nil {
						t.Errorf("%s not removed", filepath.Base(p))
					}
				}
			}
		})
	}
}
