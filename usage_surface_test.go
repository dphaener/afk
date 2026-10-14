package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/daemonconfig"
	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/usagestate"
)

// TestRefreshUsageForSurface covers the entry point the dashboard calls.
// Load-bearing rows: "saved" (usage.json holds the reading and its decision,
// and it is the only file in the state folder, because governor.json is the
// daemon's), "unsupported" and the three failure rows (nothing is written and
// the old reading stays, so a failed refresh never blanks the header), "cli
// missing" (told apart from any other probe error, because the advice
// differs) and "save fails" (the reading is returned though it was not kept).
func TestRefreshUsageForSurface(t *testing.T) {
	good := provider.UsageSnapshot{At: usageNow, Windows: usageWindows(30, 71, 13)}
	tests := []struct {
		name      string
		fake      fakeProvider
		failWrite bool
		want      usageRefreshOutcome
		wantText  string
		wantFile  bool // usage.json is the new reading
	}{
		{
			name:     "saved",
			fake:     fakeProvider{name: "fake", snap: good},
			want:     usageRefreshed,
			wantText: "Usage reading saved.",
			wantFile: true,
		},
		{
			name:     "unsupported",
			fake:     fakeProvider{name: "fake", err: provider.ErrUsageUnsupported},
			want:     usageRefreshUnsupported,
			wantText: "This agent cannot report how much of its usage is spent.",
		},
		{
			name:     "cli missing",
			fake:     fakeProvider{name: "fake", err: fmt.Errorf("running the probe: %w", exec.ErrNotFound)},
			want:     usageRefreshFailed,
			wantText: "The agent's command-line tool was not found. Install it, then try again.",
		},
		{
			name:     "other probe error",
			fake:     fakeProvider{name: "fake", err: errors.New("exit status 1")},
			want:     usageRefreshFailed,
			wantText: "Usage could not be read. Check that the agent's command-line tool is installed and logged in with an interactive login.",
		},
		{
			name:     "no usable report",
			fake:     fakeProvider{name: "fake", err: fmt.Errorf("%w: no line carries a usage_report", provider.ErrNoUsageReport)},
			want:     usageRefreshFailed,
			wantText: "Usage could not be read. Check that the agent's command-line tool is installed and logged in with an interactive login.",
		},
		{
			name:      "save fails",
			fake:      fakeProvider{name: "fake", snap: good},
			failWrite: true,
			want:      usageRefreshSaveFailed,
			wantText:  "The reading could not be saved. Check that afk's state folder can be written to.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			st := usagestate.Deps{Getenv: func(k string) string {
				if k == "XDG_STATE_HOME" {
					return dir
				}
				return ""
			}}
			old := storedFile("fake", 20*time.Minute)
			if err := usagestate.Write(st, old); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "afk", "usage.json")
			before, _ := os.ReadFile(path)
			if tt.failWrite {
				st.WriteFile = func(string, []byte, os.FileMode) error { return errors.New("disk full") }
			}
			fake := tt.fake
			d := usageDeps{
				Run: func(context.Context, string, ...string) ([]byte, error) {
					t.Fatal("real runner reached")
					return nil, nil
				},
				Now:   func() time.Time { return usageNow },
				State: st,
			}
			res := refreshUsageForSurface(&fake, daemonconfig.Governor{PauseAt: 90, LaunchBelow: 75, StaleAfter: 30 * time.Minute, ProbeInterval: 5 * time.Minute, OnUnknownUsage: "run"}, d)

			if res.Outcome != tt.want {
				t.Errorf("outcome = %d, want %d", res.Outcome, tt.want)
			}
			if res.Text != tt.wantText {
				t.Errorf("text = %q, want %q", res.Text, tt.wantText)
			}
			if strings.Contains(strings.ToLower(res.Text), "afk usage:") {
				t.Errorf("text %q carries the command prefix", res.Text)
			}
			if fake.calls != 1 {
				t.Errorf("probe calls = %d, want 1", fake.calls)
			}
			if res.Provider != "fake" {
				t.Errorf("provider = %q", res.Provider)
			}
			if tt.want != usageRefreshed && res.Err == nil {
				t.Error("a refresh that did not succeed carries no error")
			}

			after, _ := os.ReadFile(path)
			if tt.wantFile {
				if string(after) == string(before) {
					t.Fatal("usage.json was not rewritten")
				}
				f, ok := usagestate.Read(st)
				if !ok || f.Governor == nil || f.Governor.Action != "run" || !f.Governor.At.Equal(usageNow) {
					t.Errorf("stored file = %+v (ok=%v), want a reading with a run decision", f, ok)
				}
				if len(f.Windows) != len(good.Windows) {
					t.Errorf("stored %d windows, want %d", len(f.Windows), len(good.Windows))
				}
				if len(res.Snapshot.Windows) != len(good.Windows) {
					t.Errorf("result holds %d windows, want %d", len(res.Snapshot.Windows), len(good.Windows))
				}
				entries, err := os.ReadDir(filepath.Join(dir, "afk"))
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 1 || entries[0].Name() != "usage.json" {
					var names []string
					for _, e := range entries {
						names = append(names, e.Name())
					}
					t.Errorf("state folder holds %v, want only usage.json", names)
				}
				return
			}
			if string(after) != string(before) {
				t.Error("usage.json changed, want the old reading intact")
			}
			if tt.want == usageRefreshSaveFailed && len(res.Snapshot.Windows) == 0 {
				t.Error("a save failure should still return the reading")
			}
		})
	}
}
