package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/host"
	"github.com/dphaener/afk/internal/palette"
	"github.com/dphaener/afk/internal/rex"
)

// rexTestProject is a project with a main checkout and one other worktree.
// git is never run: the fake Run answers `git worktree list --porcelain`
// with the two folders, which is all Spawn asks git.
type rexTestProject struct {
	repo, sibling, own string
}

func newRexTestProject(t *testing.T) rexTestProject {
	t.Helper()
	root := t.TempDir()
	p := rexTestProject{
		repo:    filepath.Join(root, "proj"),
		sibling: filepath.Join(root, "proj.s2"),
		own:     filepath.Join(root, "proj.s1"),
	}
	for _, d := range []string{p.repo, p.sibling, p.own} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func (p rexTestProject) run(dir, name string, args ...string) (string, error) {
	if name == "git" && strings.Join(args, " ") == "worktree list --porcelain" {
		return fmt.Sprintf("worktree %s\nbranch refs/heads/main\n\nworktree %s\nbranch refs/heads/s2\n\nworktree %s\nbranch refs/heads/s1\n\n",
			p.repo, p.sibling, p.own), nil
	}
	return "", fmt.Errorf("unexpected command %s %v", name, args)
}

// recordSibling writes a sibling session's host.json naming a Rex session.
func recordSibling(t *testing.T, worktree, name, sessionID string) {
	t.Helper()
	if err := host.WriteMeta(worktree, name, host.Meta{
		PID: 1, Runtime: host.RuntimeRex,
		Rex: &host.RexBlock{SessionID: sessionID, BlockID: "block:sib"},
	}); err != nil {
		t.Fatal(err)
	}
}

// hostComesUp is a Sleep stand-in that, on its first call, writes the
// host.json the stdio host inside the new window would write, recording the
// block id Rex handed back.
func hostComesUp(t *testing.T, worktree, name, sessionID, blockID string) func(time.Duration) {
	var once sync.Once
	return func(time.Duration) {
		once.Do(func() {
			if err := host.WriteMeta(worktree, name, host.Meta{
				PID: 4242, Socket: "/tmp/afk-test.sock", Runtime: host.RuntimeRex,
				Rex: &host.RexBlock{SessionID: sessionID, BlockID: blockID},
			}); err != nil {
				t.Error(err)
			}
		})
	}
}

func rexFound(string) (string, error) { return "/usr/bin/rex", nil }

func rexMissing(string) (string, error) { return "", errors.New("not found") }

func methods(calls []rex.FakeCall) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, c.Method)
	}
	return out
}

func callArgs(t *testing.T, calls []rex.FakeCall, method string) map[string]any {
	t.Helper()
	for _, c := range calls {
		if c.Method == method {
			var m map[string]any
			if err := json.Unmarshal(c.Args, &m); err != nil {
				t.Fatal(err)
			}
			return m
		}
	}
	t.Fatalf("no %s call in %v", method, methods(calls))
	return nil
}

func spawnDeps(p rexTestProject) Deps {
	return Deps{
		Run:        p.run,
		Executable: func() (string, error) { return "/opt/afk/afk", nil },
		Now:        func() time.Time { return time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC) },
	}
}

// TestRexRuntime drives the Rex runtime's Spawn, Undo and Stop against
// rex.Fake. Load-bearing rows:
//   - "first session": no sibling recorded a session, so one is created
//     with the project's name, and the window is 160x48, does not exit on
//     completion, carries the theme and runs `afk _host --stdio`.
//   - "second session": a sibling's recorded id that session.list shows is
//     reused, and no session is created.
//   - "recreated": a recorded id session.list no longer shows (closed by
//     hand, or a server restart) gets a new session.
//   - "label clash": two listed sessions share the project's name; the
//     recorded id decides, never the label.
//   - the "refused" rows: a carried config error and a missing rex make no
//     Rex call at all, and a silent server only the status check; none
//     writes a file or falls back to afk.
//   - "host never starts": the window is closed and the new marker removed,
//     so a failed start leaves nothing behind in Rex.
//   - "stop order": the window is found before StopHost and closed after it
//     returns, read from the order of Fake.Calls, and the session is never
//     destroyed.
func TestRexRuntime(t *testing.T) {
	const newWindow = `{"window_id":"window:w1","block_ids":["block:b1"],"revision":1}`
	spawnRows := []struct {
		name        string
		recorded    string // sibling's recorded session id; "" none
		list        string // session.list reply
		planning    bool
		wantMethods []string
		wantSession string
		wantLabel   string
		wantCreate  string // label of a created session; "" none
	}{
		{
			name:        "first session",
			wantMethods: []string{"server.status", "session.create", "session.new_window"},
			wantSession: "session:new", wantLabel: "s1 · plan", wantCreate: "proj",
		},
		{
			name: "second session", recorded: "session:old",
			list:        `{"sessions":[{"session_id":"session:old","label":"proj"}]}`,
			wantMethods: []string{"server.status", "session.list", "session.new_window"},
			wantSession: "session:old", wantLabel: "s1 · plan",
		},
		{
			name: "recreated", recorded: "session:old",
			list:        `{"sessions":[{"session_id":"session:other","label":"proj"}]}`,
			wantMethods: []string{"server.status", "session.list", "session.create", "session.new_window"},
			wantSession: "session:new", wantLabel: "s1 · plan", wantCreate: "proj",
		},
		{
			name: "label clash", recorded: "session:old",
			list:        `{"sessions":[{"session_id":"session:a","label":"proj"},{"session_id":"session:old","label":"proj"}]}`,
			wantMethods: []string{"server.status", "session.list", "session.new_window"},
			wantSession: "session:old", wantLabel: "s1 · plan",
		},
		{
			name: "planning session", planning: true,
			wantMethods: []string{"server.status", "session.create", "session.new_window"},
			wantSession: "session:new", wantLabel: "plan", wantCreate: "proj",
		},
	}
	for _, tt := range spawnRows {
		t.Run(tt.name, func(t *testing.T) {
			p := newRexTestProject(t)
			name, wt := "s1", p.own
			if tt.planning {
				name, wt = PlanSessionName, p.repo
			} else if err := seedStatus(wt, Config{Name: name}, "main", "", time.Now); err != nil {
				t.Fatal(err)
			}
			if tt.recorded != "" {
				recordSibling(t, p.sibling, "s2", tt.recorded)
			}
			fake := (&rex.Fake{}).
				On("server.status", `{"api_revision":7,"instance_id":"i1"}`, nil).
				On("session.list", tt.list, nil).
				On("session.create", `{"session_id":"session:new"}`, nil).
				On("session.new_window", newWindow, nil)
			rt := NewRexRuntime(RexDeps{
				Caller: fake, LookPath: rexFound, Theme: palette.Tide,
				HostWait: time.Second, Sleep: hostComesUp(t, wt, name, tt.wantSession, "block:b1"),
			})

			h, err := rt.Spawn(HostSpawn{Name: name, Worktree: wt, MasterPlan: tt.planning}, spawnDeps(p))
			if err != nil {
				t.Fatalf("Spawn: %v", err)
			}
			calls := fake.Calls()
			if got := methods(calls); !reflect.DeepEqual(got, tt.wantMethods) {
				t.Fatalf("calls = %v, want %v", got, tt.wantMethods)
			}
			if tt.wantCreate != "" {
				if got := callArgs(t, calls, "session.create")["label"]; got != tt.wantCreate {
					t.Errorf("created session label = %v, want %q", got, tt.wantCreate)
				}
			}
			args := callArgs(t, calls, "session.new_window")
			if args["session_id"] != tt.wantSession {
				t.Errorf("window session = %v, want %s", args["session_id"], tt.wantSession)
			}
			if args["window_label"] != tt.wantLabel {
				t.Errorf("window label = %v, want %q", args["window_label"], tt.wantLabel)
			}
			opts := args["layout"].(map[string]any)["block"].(map[string]any)["options"].(map[string]any)
			if got := opts["size"]; !reflect.DeepEqual(got, map[string]any{"columns": 160.0, "rows": 48.0}) {
				t.Errorf("size = %v, want 160x48", got)
			}
			if got := opts["exit"]; !reflect.DeepEqual(got, map[string]any{"on_completion": false}) {
				t.Errorf("exit = %v, want on_completion false", got)
			}
			wantTheme := map[string]any{"foreground": palette.Tide.Fg, "background": palette.Tide.Bg, "cursor": palette.Tide.Accent, "scheme": "dark"}
			if got := opts["theme"]; !reflect.DeepEqual(got, wantTheme) {
				t.Errorf("theme = %v, want %v", got, wantTheme)
			}
			if opts["cwd"] != wt {
				t.Errorf("cwd = %v, want %s", opts["cwd"], wt)
			}
			cmd, _ := opts["command"].([]any)
			if len(cmd) < 3 || cmd[0] != "/opt/afk/afk" || cmd[1] != "_host" || cmd[2] != "--stdio" {
				t.Errorf("command = %v, want /opt/afk/afk _host --stdio ...", cmd)
			}
			if opts["shell"] != "none" {
				t.Errorf("shell = %v, want none", opts["shell"])
			}
			if h.PID != 4242 || h.MetaPath != hostMetaPath(wt, name) {
				t.Errorf("host = %+v, want pid 4242 and the session's host.json", h)
			}
			if word, ok := readRuntimeMarker(wt, name); !ok || word != "rex" {
				t.Errorf("runtime marker = %q, %v; want rex", word, ok)
			}
			if rt, err := RuntimeFor(wt, name); err != nil || rt.Name() != "rex" {
				t.Errorf("RuntimeFor after Spawn = %v, %v; want rex", rt, err)
			}
		})
	}

	refusedRows := []struct {
		name        string
		configErr   error
		lookPath    func(string) (string, error)
		status      string
		statusErr   error
		wantMethods []string
		wantInMsg   string
	}{
		{name: "refused config error", configErr: errors.New("config.toml: [runtime] backend must be \"afk\" or \"rex\""), lookPath: rexFound,
			wantInMsg: "[runtime] backend"},
		{name: "refused not installed", lookPath: rexMissing, wantInMsg: "Rex is not installed"},
		{name: "refused no answer", lookPath: rexFound, statusErr: &rex.CallError{Method: "server.status", Err: context.DeadlineExceeded},
			wantMethods: []string{"server.status"}, wantInMsg: "does not answer"},
		{name: "refused too old", lookPath: rexFound, status: `{"api_revision":5}`,
			wantMethods: []string{"server.status"}, wantInMsg: "update Rex"},
	}
	for _, tt := range refusedRows {
		t.Run(tt.name, func(t *testing.T) {
			p := newRexTestProject(t)
			fake := (&rex.Fake{}).On("server.status", tt.status, tt.statusErr)
			rt := NewRexRuntime(RexDeps{Caller: fake, LookPath: tt.lookPath, ConfigErr: tt.configErr,
				Sleep: func(time.Duration) { t.Error("refused spawn waited for a host") }})
			_, err := rt.Spawn(HostSpawn{Name: "s1", Worktree: p.own}, spawnDeps(p))
			if err == nil || !strings.Contains(err.Error(), tt.wantInMsg) {
				t.Fatalf("err = %v, want one containing %q", err, tt.wantInMsg)
			}
			t.Logf("refusal: %v", err)
			got := methods(fake.Calls())
			t.Logf("rex calls recorded: %d %v", len(got), got)
			if len(got) != len(tt.wantMethods) || (len(got) > 0 && !reflect.DeepEqual(got, tt.wantMethods)) {
				t.Errorf("calls = %v, want %v", got, tt.wantMethods)
			}
			if _, err := os.Stat(filepath.Join(p.own, ".afk", "s1")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("a refused spawn wrote .afk/s1 (stat err %v)", err)
			}
		})
	}

	t.Run("host never starts", func(t *testing.T) {
		p := newRexTestProject(t)
		fake := (&rex.Fake{}).
			On("server.status", `{"api_revision":7}`, nil).
			On("session.create", `{"session_id":"session:new"}`, nil).
			On("session.new_window", newWindow, nil).
			On("session.close_window", `{"revision":2}`, nil)
		clock := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
		rt := NewRexRuntime(RexDeps{
			Caller: fake, LookPath: rexFound, HostWait: 2 * time.Second,
			Now:   func() time.Time { return clock },
			Sleep: func(d time.Duration) { clock = clock.Add(time.Second) },
		})
		_, err := rt.Spawn(HostSpawn{Name: "s1", Worktree: p.own}, spawnDeps(p))
		if err == nil || !strings.Contains(err.Error(), "did not start") {
			t.Fatalf("err = %v, want the host-did-not-start message", err)
		}
		want := []string{"server.status", "session.create", "session.new_window", "session.close_window"}
		if got := methods(fake.Calls()); !reflect.DeepEqual(got, want) {
			t.Fatalf("calls = %v, want %v", got, want)
		}
		if got := callArgs(t, fake.Calls(), "session.close_window"); got["window_id"] != "window:w1" || got["session_id"] != "session:new" {
			t.Errorf("closed %v, want window:w1 in session:new", got)
		}
		if _, ok := readRuntimeMarker(p.own, "s1"); ok {
			t.Error("the runtime marker was left after a failed start")
		}
	})

	t.Run("undo closes the window", func(t *testing.T) {
		for _, closeErr := range []error{nil, &rex.CallError{Method: "session.close_window", Code: 3, Stderr: "not_found"}} {
			wt := t.TempDir()
			if err := host.WriteMeta(wt, "s1", host.Meta{PID: 7, Runtime: host.RuntimeRex,
				Rex: &host.RexBlock{SessionID: "session:x", BlockID: "block:b1"}}); err != nil {
				t.Fatal(err)
			}
			fake := (&rex.Fake{}).
				On("session.list_blocks", `{"blocks":[{"block_id":"block:b1","placement":"placed","window_id":"window:w1"}]}`, nil).
				On("session.close_window", `{}`, closeErr)
			err := NewRexRuntime(RexDeps{Caller: fake}).Undo(HostProcess{PID: 7, MetaPath: hostMetaPath(wt, "s1")}, Deps{})
			if err != nil {
				t.Errorf("Undo (close err %v): %v", closeErr, err)
			}
			if got := callArgs(t, fake.Calls(), "session.close_window"); got["window_id"] != "window:w1" {
				t.Errorf("closed %v, want window:w1", got)
			}
		}
	})

	t.Run("stop order", func(t *testing.T) {
		wt := t.TempDir()
		if err := host.WriteMeta(wt, "s1", host.Meta{PID: 4242, Socket: "/tmp/afk-stop-test.sock", Runtime: host.RuntimeRex,
			Rex: &host.RexBlock{SessionID: "session:x", BlockID: "block:b1"}}); err != nil {
			t.Fatal(err)
		}
		fake := (&rex.Fake{}).
			On("session.list_blocks", `{"blocks":[{"block_id":"block:b1","placement":"placed","window_id":"window:w1"}]}`, nil).
			On("test.host_stopped", `{}`, nil).
			On("session.close_window", `{"revision":3}`, nil)
		stopped := false
		cd := testCleanDeps(t, t.TempDir())
		cd.Alive = func(int) bool { return !stopped }
		cd.SignalKill = func(int, syscall.Signal) error { t.Error("the host answered Exit; no signal is needed"); return nil }
		cd.DialExit = func(string, time.Duration) error {
			// The marker call puts the host's stop into Fake.Calls, so the
			// order of the Rex calls around it is one list.
			_, _ = fake.Call(context.Background(), "test.host_stopped", struct{}{})
			stopped = true
			return nil
		}
		res, err := NewRexRuntime(RexDeps{Caller: fake}).Stop(wt, "s1", cd)
		if err != nil || res != HostStopped {
			t.Fatalf("Stop = %v, %v; want HostStopped", res, err)
		}
		want := []string{"session.list_blocks", "test.host_stopped", "session.close_window"}
		if got := methods(fake.Calls()); !reflect.DeepEqual(got, want) {
			t.Fatalf("calls = %v, want %v (window found before StopHost, closed after, session never destroyed)", got, want)
		}
		if _, err := os.Stat(hostMetaPath(wt, "s1")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("host.json still there after Stop (stat err %v)", err)
		}
	})

	t.Run("stop with the window already gone", func(t *testing.T) {
		wt := t.TempDir()
		if err := host.WriteMeta(wt, "s1", host.Meta{PID: 4242, Runtime: host.RuntimeRex,
			Rex: &host.RexBlock{SessionID: "session:x", WindowID: "window:w1", BlockID: "block:b1"}}); err != nil {
			t.Fatal(err)
		}
		fake := (&rex.Fake{}).On("session.close_window", `{}`, &rex.CallError{Method: "session.close_window", Code: 3, Stderr: "not_found"})
		cd := testCleanDeps(t, t.TempDir())
		cd.Alive = func(int) bool { return false }
		res, err := NewRexRuntime(RexDeps{Caller: fake}).Stop(wt, "s1", cd)
		if err != nil || res != HostGone {
			t.Fatalf("Stop = %v, %v; want HostGone with no error", res, err)
		}
		if got := methods(fake.Calls()); !reflect.DeepEqual(got, []string{"session.close_window"}) {
			t.Errorf("calls = %v, want only session.close_window (recorded window id, no lookup)", got)
		}
		if s := cd.Stderr.(*strings.Builder).String(); s != "" {
			t.Errorf("a window already gone printed %q", s)
		}
	})
}

// TestTerminalTheme pins the scheme read from the background's brightness.
// Load-bearing rows: tide (the default, dark) and paper (light), since a
// wrong scheme makes Rex pick unreadable defaults for the colours afk leaves
// to it.
func TestTerminalTheme(t *testing.T) {
	tests := []struct {
		name  string
		theme palette.Theme
		want  string
	}{
		{"tide", palette.Tide, "dark"},
		{"fjord", palette.Fjord, "dark"},
		{"paper", palette.Paper, "light"},
		{"phosphor", palette.Phosphor, "dark"},
		{"unparseable background", palette.Theme{Bg: "white"}, "dark"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := terminalTheme(tt.theme)
			if got.Scheme != tt.want || got.Background != tt.theme.Bg || got.Foreground != tt.theme.Fg || got.Cursor != tt.theme.Accent {
				t.Errorf("terminalTheme = %+v, want scheme %s and the theme's colours", got, tt.want)
			}
		})
	}
}
