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

	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/usagestate"
	"github.com/dphaener/afk/internal/web"
)

// TestWebRefreshUsage checks that each way a reading can end maps to the right
// web outcome and sentence, over fake dependencies. Load-bearing rows: "no
// agent" and "save fails" both fold into failed with a plain sentence (the
// page has no fourth state), "unsupported" stays apart from failed because the
// page words them differently, and "saved" must land in the state folder the
// daemon passed in. Every text is checked for provider names.
func TestWebRefreshUsage(t *testing.T) {
	good := provider.UsageSnapshot{At: usageNow, Windows: usageWindows(30, 71, 13)}
	tests := []struct {
		name       string
		fake       fakeProvider
		lookupErr  error
		failWrite  bool
		want       string
		wantText   string
		wantWrites bool
		wantCalls  int
	}{
		{name: "saved", fake: fakeProvider{name: "fake", snap: good}, want: web.UsageRefreshed, wantText: "Usage reading saved.", wantWrites: true, wantCalls: 1},
		{name: "unsupported", fake: fakeProvider{name: "fake", err: provider.ErrUsageUnsupported}, want: web.UsageRefreshUnsupported, wantText: "This agent cannot report how much of its usage is spent.", wantCalls: 1},
		{name: "cli missing", fake: fakeProvider{name: "fake", err: fmt.Errorf("run: %w", exec.ErrNotFound)}, want: web.UsageRefreshFailed, wantText: "The agent's command-line tool was not found. Install it, then try again.", wantCalls: 1},
		{name: "probe error", fake: fakeProvider{name: "fake", err: errors.New("exit status 1")}, want: web.UsageRefreshFailed, wantText: "Usage could not be read. Check that the agent's command-line tool is installed and logged in with an interactive login.", wantCalls: 1},
		{name: "save fails", fake: fakeProvider{name: "fake", snap: good}, failWrite: true, want: web.UsageRefreshFailed, wantText: "The reading could not be saved. Check that afk's state folder can be written to.", wantCalls: 1},
		{name: "no agent", lookupErr: errors.New("no such agent"), want: web.UsageRefreshFailed, wantText: "No agent is set up to read usage from. Run afk usage in a terminal to see why."},
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
			got := webRefreshUsageWith(d)
			if got.Outcome != tt.want || got.Text != tt.wantText {
				t.Fatalf("got %+v, want outcome %q text %q", got, tt.want, tt.wantText)
			}
			for _, name := range []string{"claude", "codex", "opencode", "anthropic", "openai"} {
				if strings.Contains(strings.ToLower(got.Text), name) {
					t.Errorf("text %q names %q", got.Text, name)
				}
			}
			if fake.calls != tt.wantCalls {
				t.Errorf("probe calls = %d, want %d", fake.calls, tt.wantCalls)
			}
			_, err := os.Stat(filepath.Join(dir, "afk", "usage.json"))
			if wrote := err == nil; wrote != tt.wantWrites {
				t.Errorf("usage.json written = %v, want %v", wrote, tt.wantWrites)
			}
			if _, err := os.Stat(filepath.Join(dir, "afk", "governor.json")); err == nil {
				t.Error("governor.json was written; it is the daemon's")
			}
		})
	}
}

// TestWebRefreshUsageWired checks that daemonWebDeps sets the seam; without it
// the page would draw no button and both POSTs would answer 404.
func TestWebRefreshUsageWired(t *testing.T) {
	d := daemonWebDeps(nil, t.TempDir(), usagestate.Deps{}, nil)
	if d.RefreshUsage == nil {
		t.Fatal("daemonWebDeps left RefreshUsage nil")
	}
}
