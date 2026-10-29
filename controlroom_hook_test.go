package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/daemonconfig"
	"github.com/dphaener/afk/internal/rex"
)

// TestControlRoom drives the hook against rex.Fake. Load-bearing rows: "backend
// afk" and "bad runtime" (an afk-only machine must make no Rex call at all),
// "server down" (no create while Rex is away), "restart within a minute" (a
// closed window must not be re-created in a loop) and "failed create" (a
// failure is logged, never raised, and never panics).
func TestControlRoom(t *testing.T) {
	const status = `{"api_revision":7,"instance_id":"x","session_count":1}`
	const sessions = `{"sessions":[{"session_id":"s1","label":"afk"}]}`
	const placed = `{"blocks":[{"block_id":"b1","placement":"placed","window_id":"w1"}]}`
	const detached = `{"blocks":[{"block_id":"b1","placement":"detached"}]}`
	const window = `{"window_id":"w2","block_ids":["b2"]}`
	t0 := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		cfg       daemonconfig.Config
		cfgErr    error
		script    func(*rex.Fake)
		last      time.Time
		wantCalls []string
		wantLog   string
	}{
		{
			name:   "backend afk makes no call",
			cfg:    daemonconfig.Config{Runtime: daemonconfig.RuntimeConfig{Backend: "afk"}},
			script: func(*rex.Fake) {},
		},
		{
			name:   "bad runtime makes no call",
			cfg:    daemonconfig.Config{Runtime: daemonconfig.RuntimeConfig{Backend: "rex"}, RuntimeErr: errors.New("bad")},
			script: func(*rex.Fake) {},
		},
		{
			name:   "config load error makes no call",
			cfgErr: errors.New("unreadable"),
			script: func(*rex.Fake) {},
		},
		{
			name:      "server down makes no create",
			cfg:       rexCfg(),
			script:    func(f *rex.Fake) { f.On("server.status", "", errors.New("refused")) },
			wantCalls: []string{"server.status"},
			wantLog:   "not answering",
		},
		{
			name: "missing session creates the session and one window",
			cfg:  rexCfg(),
			script: func(f *rex.Fake) {
				f.On("server.status", status, nil).On("session.list", `{"sessions":[]}`, nil).
					On("session.create", `{"session_id":"s1"}`, nil).On("session.new_window", window, nil)
			},
			wantCalls: []string{"server.status", "session.list", "session.create", "session.new_window"},
			wantLog:   "opened",
		},
		{
			name: "dead window creates one window",
			cfg:  rexCfg(),
			script: func(f *rex.Fake) {
				f.On("server.status", status, nil).On("session.list", sessions, nil).
					On("session.list_blocks", detached, nil).On("session.new_window", window, nil)
			},
			wantCalls: []string{"server.status", "session.list", "session.list_blocks", "session.new_window"},
			wantLog:   "opened",
		},
		{
			name: "open window does nothing",
			cfg:  rexCfg(),
			script: func(f *rex.Fake) {
				f.On("server.status", status, nil).On("session.list", sessions, nil).On("session.list_blocks", placed, nil)
			},
			wantCalls: []string{"server.status", "session.list", "session.list_blocks"},
		},
		{
			name: "restart within a minute does not start again",
			cfg:  rexCfg(),
			last: t0.Add(-30 * time.Second),
			script: func(f *rex.Fake) {
				f.On("server.status", status, nil).On("session.list", sessions, nil).On("session.list_blocks", detached, nil)
			},
			wantCalls: []string{"server.status", "session.list", "session.list_blocks"},
		},
		{
			name: "failed create is logged",
			cfg:  rexCfg(),
			script: func(f *rex.Fake) {
				f.On("server.status", status, nil).On("session.list", `{"sessions":[]}`, nil).
					On("session.create", "", errors.New("boom"))
			},
			wantCalls: []string{"server.status", "session.list", "session.create"},
			wantLog:   "could not create",
		},
		{
			name: "failed window is logged",
			cfg:  rexCfg(),
			script: func(f *rex.Fake) {
				f.On("server.status", status, nil).On("session.list", sessions, nil).
					On("session.list_blocks", detached, nil).On("session.new_window", "", errors.New("boom"))
			},
			wantCalls: []string{"server.status", "session.list", "session.list_blocks", "session.new_window"},
			wantLog:   "could not open",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &rex.Fake{}
			tt.script(f)
			var logs []string
			c := &controlRoom{
				rex:         f,
				loadRuntime: func() (daemonconfig.Config, error) { return tt.cfg, tt.cfgErr },
				now:         func() time.Time { return t0 },
				executable:  func() (string, error) { return "/bin/afk", nil },
				logf:        func(format string, a ...any) { logs = append(logs, fmt.Sprintf(format, a...)) },
				lastStart:   tt.last,
			}
			c.check(context.Background())
			var got []string
			for _, call := range f.Calls() {
				got = append(got, call.Method)
			}
			if strings.Join(got, ",") != strings.Join(tt.wantCalls, ",") {
				t.Errorf("calls = %v, want %v", got, tt.wantCalls)
			}
			if tt.wantLog == "" && len(logs) != 0 {
				t.Errorf("unexpected log: %v", logs)
			}
			if tt.wantLog != "" && !strings.Contains(strings.Join(logs, "\n"), tt.wantLog) {
				t.Errorf("log = %v, want it to contain %q", logs, tt.wantLog)
			}
		})
	}
}

func rexCfg() daemonconfig.Config {
	return daemonconfig.Config{Runtime: daemonconfig.RuntimeConfig{Backend: "rex"}}
}

// TestControlRoomRateLimit: a start records its time, so a second pass inside
// the minute (the window still dead) opens nothing, and one a minute later does.
func TestControlRoomRateLimit(t *testing.T) {
	f := &rex.Fake{}
	f.On("server.status", `{"api_revision":7}`, nil).On("session.list", `{"sessions":[{"session_id":"s1","label":"afk"}]}`, nil).
		On("session.list_blocks", `{"blocks":[]}`, nil).On("session.new_window", `{"window_id":"w","block_ids":["b"]}`, nil)
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	c := &controlRoom{
		rex:         f,
		loadRuntime: func() (daemonconfig.Config, error) { return rexCfg(), nil },
		now:         func() time.Time { return now },
		executable:  func() (string, error) { return "/bin/afk", nil },
		logf:        func(string, ...any) {},
	}
	count := func() int {
		n := 0
		for _, call := range f.Calls() {
			if call.Method == "session.new_window" {
				n++
			}
		}
		return n
	}
	c.check(context.Background())
	now = now.Add(10 * time.Second)
	c.check(context.Background())
	if count() != 1 {
		t.Fatalf("windows opened = %d after two passes in a minute, want 1", count())
	}
	now = now.Add(time.Minute)
	c.check(context.Background())
	if count() != 2 {
		t.Fatalf("windows opened = %d after a minute, want 2", count())
	}
}
