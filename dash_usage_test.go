package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/ui"
	"github.com/dphaener/afk/internal/usagestate"
)

// TestDashRefreshUsage pins the dashboard adapter over refreshUsageForSurface.
//
// Load-bearing rows: "saved" (the only row that may write usage.json),
// "save fails" (maps to failed, not refreshed, so the owner is never told a
// reading was kept when it was not), "unsupported" (kept apart from failed
// because the notice kind differs) and "lookup fails" (a plain sentence, no
// probe). Every row also checks that the real runner is never reached.
func TestDashRefreshUsage(t *testing.T) {
	good := provider.UsageSnapshot{At: usageNow, Windows: usageWindows(30, 71, 13)}
	tests := []struct {
		name       string
		fake       fakeProvider
		lookupErr  error
		failWrite  bool
		want       ui.UsageRefreshOutcome
		wantText   string
		wantWrites bool
		wantCalls  int
	}{
		{name: "saved", fake: fakeProvider{name: "fake", snap: good}, want: ui.UsageRefreshed, wantText: "Usage reading saved.", wantWrites: true, wantCalls: 1},
		{name: "unsupported", fake: fakeProvider{name: "fake", err: provider.ErrUsageUnsupported}, want: ui.UsageRefreshUnsupported, wantText: "This agent cannot report how much of its usage is spent.", wantCalls: 1},
		{name: "cli missing", fake: fakeProvider{name: "fake", err: fmt.Errorf("run: %w", exec.ErrNotFound)}, want: ui.UsageRefreshFailed, wantText: "The agent's command-line tool was not found. Install it, then try again.", wantCalls: 1},
		{name: "probe error", fake: fakeProvider{name: "fake", err: errors.New("exit status 1")}, want: ui.UsageRefreshFailed, wantText: "Usage could not be read. Check that the agent's command-line tool is installed and logged in with an interactive login.", wantCalls: 1},
		{name: "save fails", fake: fakeProvider{name: "fake", snap: good}, failWrite: true, want: ui.UsageRefreshFailed, wantText: "The reading could not be saved. Check that afk's state folder can be written to.", wantCalls: 1},
		{name: "lookup fails", lookupErr: errors.New("no such agent"), want: ui.UsageRefreshFailed, wantText: "No agent is set up to read usage from. Run afk usage in a terminal to see why."},
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
			if tt.failWrite {
				st.WriteFile = func(string, []byte, os.FileMode) error { return errors.New("disk full") }
			}
			fake := tt.fake
			d := usageDeps{
				Lookup: func(name string) (provider.Provider, error) {
					if name != "" {
						t.Errorf("lookup name = %q, want the default", name)
					}
					if tt.lookupErr != nil {
						return nil, tt.lookupErr
					}
					return &fake, nil
				},
				Run: func(context.Context, string, ...string) ([]byte, error) {
					t.Fatal("real runner reached")
					return nil, nil
				},
				Now:   func() time.Time { return usageNow },
				State: st,
			}
			got := dashRefreshUsageWith(d)
			if got.Outcome != tt.want || got.Text != tt.wantText {
				t.Fatalf("got %+v, want outcome %q text %q", got, tt.want, tt.wantText)
			}
			if fake.calls != tt.wantCalls {
				t.Errorf("probe calls = %d, want %d", fake.calls, tt.wantCalls)
			}
			_, err := os.Stat(filepath.Join(dir, "afk", "usage.json"))
			if wrote := err == nil; wrote != tt.wantWrites {
				t.Errorf("usage.json written = %v, want %v", wrote, tt.wantWrites)
			}
		})
	}
}
