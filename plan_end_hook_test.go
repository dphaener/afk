package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/decisionlog"
	"github.com/dphaener/afk/internal/planchat"
	"github.com/dphaener/afk/internal/planend"
	"github.com/dphaener/afk/internal/plansession"
	"github.com/dphaener/afk/internal/session"
)

// endFake records every call so a row can say what was and was not touched.
type endFake struct {
	alive    bool
	files    plansession.Files
	filesErr error
	chat     planchat.State
	stopRes  session.HostStop
	stopErr  error
	logErr   error

	calls  []string
	logged []decisionlog.Entry
	lines  []string
}

func (f *endFake) ender(roots ...string) planEnder {
	return planEnder{
		targets: func() []planWaitTarget {
			var out []planWaitTarget
			for _, r := range roots {
				out = append(out, planWaitTarget{Name: "app", Root: r})
			}
			return out
		},
		files: func(string) (plansession.Files, error) {
			f.calls = append(f.calls, "files")
			return f.files, f.filesErr
		},
		chat: func(string, string) planchat.Chat {
			f.calls = append(f.calls, "chat")
			return planchat.Chat{State: f.chat}
		},
		hostAlive: func(string) bool { f.calls = append(f.calls, "alive"); return f.alive },
		stop: func(string) (session.HostStop, error) {
			f.calls = append(f.calls, "stop")
			return f.stopRes, f.stopErr
		},
		logDecision: func(_ string, e decisionlog.Entry) error {
			f.calls = append(f.calls, "log")
			if f.logErr != nil {
				return f.logErr
			}
			f.logged = append(f.logged, e)
			return nil
		},
		now:  func() time.Time { return time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC) },
		logf: func(format string, a ...any) { f.lines = append(f.lines, format) },
	}
}

func finishedFake() *endFake {
	return &endFake{
		alive: true,
		files: plansession.Files{
			HasState: true,
			State:    plansession.State{Mode: plansession.ModeExtend, Phase: plansession.PhaseReview},
			Summary:  "Added two tasks.",
		},
		chat:    planchat.StateWaitingForText,
		stopRes: session.HostStopped,
	}
}

func has(calls []string, c string) bool {
	for _, x := range calls {
		if x == c {
			return true
		}
	}
	return false
}

// TestPlanEnd covers the hook's branches with fakes for every field. The
// load-bearing rows are "not finished" and "host not alive" (the chat, a
// transcript read, must never be read for them), "chat working" (ending would
// cut off a reply), "stop error" (a warning and no log line, so the retry on
// the next pass is clean) and "host gone" (nothing was ended, so nothing is
// logged).
func TestPlanEnd(t *testing.T) {
	tests := []struct {
		name       string
		mut        func(*endFake)
		wantStop   bool
		wantLog    int
		wantNoRead []string
		wantLines  int
	}{
		{name: "ends and logs one entry", mut: func(*endFake) {}, wantStop: true, wantLog: 1, wantLines: 1},
		{name: "host gone logs nothing", mut: func(f *endFake) { f.stopRes = session.HostGone }, wantStop: true},
		{name: "stop error warns, no log", mut: func(f *endFake) { f.stopErr = errors.New("boom") }, wantStop: true, wantLines: 1},
		{name: "log error warns", mut: func(f *endFake) { f.logErr = errors.New("disk") }, wantStop: true, wantLines: 1},
		{name: "chat working", mut: func(f *endFake) { f.chat = planchat.StateWorking }},
		{name: "not finished never reads chat", mut: func(f *endFake) { f.files.State.Phase = plansession.PhaseCheck }, wantNoRead: []string{"chat"}},
		{name: "host not alive never reads files or chat", mut: func(f *endFake) { f.alive = false }, wantNoRead: []string{"files", "chat"}},
		{name: "new session approved", mut: func(f *endFake) {
			f.files.State = plansession.State{Mode: plansession.ModeNew, Phase: plansession.PhaseApproved}
		}},
		{name: "no state", mut: func(f *endFake) { f.files.HasState = false }, wantNoRead: []string{"chat"}},
		{name: "files error leaves it", mut: func(f *endFake) { f.filesErr = errors.New("bad toml") }, wantNoRead: []string{"chat"}, wantLines: 1},
		{name: "no summary", mut: func(f *endFake) { f.files.Summary = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := finishedFake()
			tt.mut(f)
			f.ender("/r").check(context.Background())
			if got := has(f.calls, "stop"); got != tt.wantStop {
				t.Fatalf("stop = %v, want %v (calls %v)", got, tt.wantStop, f.calls)
			}
			if len(f.logged) != tt.wantLog {
				t.Fatalf("logged %d entries, want %d", len(f.logged), tt.wantLog)
			}
			for _, c := range tt.wantNoRead {
				if has(f.calls, c) {
					t.Fatalf("%s was called: %v", c, f.calls)
				}
			}
			if len(f.lines) != tt.wantLines {
				t.Fatalf("lines = %v, want %d", f.lines, tt.wantLines)
			}
		})
	}
}

// TestPlanEndEntry pins the decision log line the end writes.
func TestPlanEndEntry(t *testing.T) {
	f := finishedFake()
	f.ender("/r").check(context.Background())
	if len(f.logged) != 1 {
		t.Fatalf("logged %d", len(f.logged))
	}
	e := f.logged[0]
	if e.Kind != "plan_session_ended" || e.Session != session.PlanSessionName || e.Chosen != "ended" ||
		e.ChosenBy != decisionlog.ChosenByPolicy || e.Rule != planend.Rule || e.At != "2026-10-19T12:00:00Z" {
		t.Fatalf("entry = %+v", e)
	}
	if !strings.Contains(f.lines[0], "was ended") {
		t.Fatalf("line = %q", f.lines[0])
	}
}

// TestPlanEndCancelled: a cancelled context stops before the first project.
func TestPlanEndCancelled(t *testing.T) {
	f := finishedFake()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.ender("/a", "/b").check(ctx)
	if len(f.calls) != 0 {
		t.Fatalf("calls = %v", f.calls)
	}
}

// TestPlanEndEachProject: every target is looked at, so one project's failure
// does not hide another's finished session.
func TestPlanEndEachProject(t *testing.T) {
	f := finishedFake()
	f.ender("/a", "/b").check(context.Background())
	if len(f.logged) != 2 {
		t.Fatalf("logged %d, want 2", len(f.logged))
	}
}

// TestComposePlanHooks: the wait check runs before the end check, so the owner
// is told the session is waiting before its agent goes.
func TestComposePlanHooks(t *testing.T) {
	var order []string
	h := composePlanHooks(
		func(context.Context) { order = append(order, "wait") },
		func(context.Context) { order = append(order, "end") },
	)
	h(context.Background())
	if strings.Join(order, ",") != "wait,end" {
		t.Fatalf("order = %v", order)
	}
}
