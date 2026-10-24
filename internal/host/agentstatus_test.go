package host

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/progstatus"
)

// report encodes r as the agent would print it, failing the test on a record
// the encoder refuses (a typo in a row, not a case under test).
func report(t *testing.T, r progstatus.Record) string {
	t.Helper()
	b, err := progstatus.Encode(r)
	if err != nil {
		t.Fatalf("encoding %+v: %v", r, err)
	}
	return string(b)
}

// feed runs chunks through one real Scanner into k, the way pumpPTY does, and
// reports whether any observe call said a publish is forced.
func feed(k *agentKeeper, epoch uint64, chunks []string, now time.Time) bool {
	sc := progstatus.NewScanner()
	forced := false
	for _, c := range chunks {
		if res := sc.Scan([]byte(c)); len(res.Reports) > 0 {
			if k.observe(epoch, res.Reports, now) {
				forced = true
			}
		}
	}
	return forced
}

// TestAgentKeeperObserve pins which reports become the session's status. The
// load-bearing rows: "split across two reads" (the pump's reads cut the stream
// anywhere, and only a Scanner carried across them sees the report);
// "child id record ignored" and "foreign app ignored" (a sub-task or a tool
// run in the agent's terminal must never pose as the agent); "no app kept"
// (the spec lets the program leave app out); "root clear forgets" and
// "child clear leaves the record"; and the two provider rows, which show the
// match goes through the session's own provider rather than any known agent.
func TestAgentKeeperObserve(t *testing.T) {
	working := report(t, progstatus.Record{State: progstatus.StateWorking, App: "claude-code", Msg: "Running tests"})
	idle := report(t, progstatus.Record{State: progstatus.StateIdle, App: "claude-code"})
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		provider string
		chunks   []string
		want     *AgentStatus // At is checked separately
		forced   bool
	}{
		{
			name:   "one report kept",
			chunks: []string{"hello " + working + " world"},
			want:   &AgentStatus{State: "working", Msg: "Running tests"},
			forced: true,
		},
		{
			name:   "the spec's literal bytes kept",
			chunks: []string{"\x1b]7501;state=working:app=claude-code:msg=UnVubmluZyB0ZXN0cw==\x1b\\"},
			want:   &AgentStatus{State: "working", Msg: "Running tests"},
			forced: true,
		},
		{
			// Pairs are separated by ':'; with ';' the whole body is one
			// pair whose value is not a valid state, so nothing is kept.
			name:   "semicolon separated body is not a report",
			chunks: []string{"\x1b]7501;state=working;app=claude-code;msg=UnVubmluZyB0ZXN0cw==\x1b\\"},
		},
		{
			name:   "kept across chunked output",
			chunks: []string{"line one\r\n", working, "line two\r\n", "\x1b[31mred\x1b[0m"},
			want:   &AgentStatus{State: "working", Msg: "Running tests"},
			forced: true,
		},
		{
			name:   "split across two reads",
			chunks: []string{"x" + working[:9], working[9:] + "y"},
			want:   &AgentStatus{State: "working", Msg: "Running tests"},
			forced: true,
		},
		{
			name:   "later report replaces",
			chunks: []string{working, idle},
			want:   &AgentStatus{State: "idle"},
			forced: true,
		},
		{
			name:   "kind kept",
			chunks: []string{report(t, progstatus.Record{State: progstatus.StateBlocked, Kind: progstatus.KindPermission, App: "claude-code", Msg: "Allow?"})},
			want:   &AgentStatus{State: "blocked", Kind: "permission", Msg: "Allow?"},
			forced: true,
		},
		{
			name:   "child id record ignored",
			chunks: []string{report(t, progstatus.Record{State: progstatus.StateWorking, ID: "x", App: "claude-code", Msg: "sub"})},
		},
		{
			name:   "foreign app ignored",
			chunks: []string{report(t, progstatus.Record{State: progstatus.StateWorking, App: "brew", Msg: "Installing"})},
		},
		{
			name:   "no app kept",
			chunks: []string{report(t, progstatus.Record{State: progstatus.StateDone})},
			want:   &AgentStatus{State: "done"},
			forced: true,
		},
		{
			// forced is the working report's; the clear then leaves no
			// status to publish.
			name:   "root clear forgets",
			chunks: []string{working, report(t, progstatus.Record{State: progstatus.StateClear})},
			forced: true,
		},
		{
			name:   "child clear leaves the record",
			chunks: []string{working, report(t, progstatus.Record{State: progstatus.StateClear, ID: "x"})},
			want:   &AgentStatus{State: "working", Msg: "Running tests"},
			forced: true,
		},
		{
			name:     "another provider's app ignored",
			provider: "codex",
			chunks:   []string{working},
		},
		{
			name:     "unknown session provider keeps only reports with no app",
			provider: "no-such-agent",
			chunks:   []string{working, report(t, progstatus.Record{State: progstatus.StateIdle})},
			want:     &AgentStatus{State: "idle"},
			forced:   true,
		},
		{
			name:   "plain output keeps nothing",
			chunks: []string{"just text\r\n", "\x1b]0;title\x07"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k := newAgentKeeper(agentStatusPolicy{publish: 2 * time.Second, tick: time.Second}, tt.provider)
			forced := feed(k, 0, tt.chunks, now)
			if forced != tt.forced {
				t.Errorf("forced = %v, want %v", forced, tt.forced)
			}
			snap, ok := k.publishDue(now)
			got := snap.Status
			if tt.want == nil {
				if ok && got != nil {
					t.Fatalf("status = %+v, want none", got)
				}
				return
			}
			if !ok || got == nil {
				t.Fatalf("no status published, want %+v", tt.want)
			}
			if got.At != "2026-10-23T12:00:00Z" {
				t.Errorf("At = %q, want the time the host saw the report", got.At)
			}
			got.At = ""
			if *got != *tt.want {
				t.Errorf("status = %+v, want %+v", *got, *tt.want)
			}
		})
	}
}

// TestAgentKeeperPublishTiming pins the publish schedule under a driven
// clock. Load-bearing rows: "msg change inside the interval is held, then
// published" (the 2-second cap that bounds host.json churn and the web
// page's change events), "state change publishes at once" (the change people
// look for is never held), "no change publishes nothing" and "a resend is no
// change" (an agent repeating itself must not rewrite the file), and the
// reset and exit rows, which a restart in place relies on.
func TestAgentKeeperPublishTiming(t *testing.T) {
	type step struct {
		at      time.Duration
		send    *progstatus.Record
		reset   bool
		exit    *LastExit
		publish bool
		// state and msg are what the publish carries, "" for no status.
		state, msg string
	}
	rec := func(s progstatus.State, msg string) *progstatus.Record {
		return &progstatus.Record{State: s, App: "claude-code", Msg: msg}
	}
	tests := []struct {
		name  string
		steps []step
	}{
		{
			name: "msg change inside the interval is held, then published",
			steps: []step{
				{at: 0, send: rec(progstatus.StateWorking, "one"), publish: true, state: "working", msg: "one"},
				{at: 500 * time.Millisecond, send: rec(progstatus.StateWorking, "two")},
				{at: 1999 * time.Millisecond},
				{at: 2 * time.Second, publish: true, state: "working", msg: "two"},
				{at: 3 * time.Second},
			},
		},
		{
			name: "state change publishes at once",
			steps: []step{
				{at: 0, send: rec(progstatus.StateWorking, "one"), publish: true, state: "working", msg: "one"},
				{at: 100 * time.Millisecond, send: rec(progstatus.StateIdle, ""), publish: true, state: "idle"},
			},
		},
		{
			name: "no change publishes nothing",
			steps: []step{
				{at: 0},
				{at: 5 * time.Second},
			},
		},
		{
			name: "a resend is no change",
			steps: []step{
				{at: 0, send: rec(progstatus.StateWorking, "one"), publish: true, state: "working", msg: "one"},
				{at: 3 * time.Second, send: rec(progstatus.StateWorking, "one")},
				{at: 6 * time.Second},
			},
		},
		{
			name: "clear publishes at once",
			steps: []step{
				{at: 0, send: rec(progstatus.StateWorking, "one"), publish: true, state: "working", msg: "one"},
				{at: 100 * time.Millisecond, send: &progstatus.Record{State: progstatus.StateClear}, publish: true},
			},
		},
		{
			name: "reset publishes the status gone at once",
			steps: []step{
				{at: 0, send: rec(progstatus.StateWorking, "one"), publish: true, state: "working", msg: "one"},
				{at: 100 * time.Millisecond, reset: true, publish: true},
			},
		},
		{
			name: "exit publishes at once",
			steps: []step{
				{at: 0, send: rec(progstatus.StateWorking, "one"), publish: true, state: "working", msg: "one"},
				{at: 100 * time.Millisecond, exit: &LastExit{Code: 3, At: "x"}, publish: true, state: "working", msg: "one"},
			},
		},
	}
	base := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k := newAgentKeeper(agentStatusPolicy{publish: 2 * time.Second, tick: time.Second}, "")
			for i, s := range tt.steps {
				now := base.Add(s.at)
				if s.send != nil {
					k.observe(k.currentEpoch(), []progstatus.Report{{Record: *s.send}}, now)
				}
				if s.reset {
					k.reset()
				}
				if s.exit != nil {
					k.recordExit(*s.exit)
				}
				snap, ok := k.publishDue(now)
				if ok != s.publish {
					t.Fatalf("step %d (%s): published = %v, want %v", i, s.at, ok, s.publish)
				}
				if !ok {
					continue
				}
				var state, msg string
				if snap.Status != nil {
					state, msg = snap.Status.State, snap.Status.Msg
				}
				if state != s.state || msg != s.msg {
					t.Errorf("step %d (%s): published state %q msg %q, want %q %q", i, s.at, state, msg, s.state, s.msg)
				}
				if s.exit != nil && (snap.Exit == nil || *snap.Exit != *s.exit) {
					t.Errorf("step %d: exit = %+v, want %+v", i, snap.Exit, *s.exit)
				}
			}
		})
	}
}

// TestAgentKeeperNextWake pins that a held change is woken for when it falls
// due rather than up to a tick later, and that an idle keeper sleeps a tick.
func TestAgentKeeperNextWake(t *testing.T) {
	base := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	k := newAgentKeeper(agentStatusPolicy{publish: 2 * time.Second, tick: time.Second}, "")
	if got := k.nextWake(base); got != time.Second {
		t.Errorf("idle nextWake = %s, want one tick", got)
	}
	k.observe(0, []progstatus.Report{{Record: progstatus.Record{State: progstatus.StateWorking, Msg: "a"}}}, base)
	k.publishDue(base)
	k.observe(0, []progstatus.Report{{Record: progstatus.Record{State: progstatus.StateWorking, Msg: "b"}}}, base.Add(1700*time.Millisecond))
	if got := k.nextWake(base.Add(1700 * time.Millisecond)); got != 300*time.Millisecond {
		t.Errorf("held nextWake = %s, want the 300ms left of the interval", got)
	}
}

// TestAgentKeeperIgnoresARetiredGeneration is load-bearing for restarts: the
// retired child's pump can still hand over its last bytes after reset, and
// they must not become the resumed agent's status.
func TestAgentKeeperIgnoresARetiredGeneration(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	k := newAgentKeeper(agentStatusPolicy{publish: 2 * time.Second, tick: time.Second}, "")
	old := k.currentEpoch()
	k.reset()
	r := []progstatus.Report{{Record: progstatus.Record{State: progstatus.StateWorking}}}
	if k.observe(old, r, now) {
		t.Errorf("a retired generation's report forced a publish")
	}
	if _, ok := k.publishDue(now); ok {
		t.Errorf("a retired generation's report was published")
	}
	if !k.observe(k.currentEpoch(), r, now) {
		t.Errorf("the current generation's report did not force a publish")
	}
}

// TestLastExitOf pins last_exit against real reaped processes. Load-bearing
// rows: "exit 3" (a code and no signal) and "SIGTERM" (-1 and the signal's
// conventional name, which is what someone searching a log types).
func TestLastExitOf(t *testing.T) {
	requireSh(t)
	at := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		script string
		want   LastExit
	}{
		{name: "exit 3", script: "exit 3", want: LastExit{Code: 3, At: "2026-10-23T12:00:00Z"}},
		{name: "exit 0", script: "exit 0", want: LastExit{Code: 0, At: "2026-10-23T12:00:00Z"}},
		{name: "SIGTERM", script: "kill -TERM $$", want: LastExit{Code: -1, Signal: "SIGTERM", At: "2026-10-23T12:00:00Z"}},
		{name: "SIGKILL", script: "kill -KILL $$", want: LastExit{Code: -1, Signal: "SIGKILL", At: "2026-10-23T12:00:00Z"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command("/bin/sh", "-c", tt.script)
			_ = cmd.Run()
			if got := lastExitOf(cmd.ProcessState, at); got != tt.want {
				t.Errorf("lastExitOf = %+v, want %+v", got, tt.want)
			}
		})
	}
	t.Run("no process state", func(t *testing.T) {
		if got, want := lastExitOf(nil, at), (LastExit{Code: -1, At: "2026-10-23T12:00:00Z"}); got != want {
			t.Errorf("lastExitOf(nil) = %+v, want %+v", got, want)
		}
	})
}

// TestMetaAgentStatusAndLastExitAreOptional pins the additive rule: a
// host.json written before these fields existed reads with both nil, and a
// Meta without them writes neither key, so older readers see the file they
// always did.
func TestMetaAgentStatusAndLastExitAreOptional(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(repo, ".afk", "old")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := `{"pid": 1, "socket": "/tmp/s", "started_at": "2026-01-01T00:00:00Z", "argv": ["x"]}`
	if err := os.WriteFile(filepath.Join(dir, "host.json"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := ReadMeta(repo, "old")
	if err != nil {
		t.Fatalf("ReadMeta of an old host.json: %v", err)
	}
	if m.AgentStatus != nil || m.LastExit != nil {
		t.Errorf("old host.json read as agent_status %+v, last_exit %+v; want both nil", m.AgentStatus, m.LastExit)
	}

	data, err := json.Marshal(Meta{PID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if s := string(data); strings.Contains(s, "agent_status") || strings.Contains(s, "last_exit") {
		t.Errorf("a Meta without the fields marshals as %s; want neither key", s)
	}

	data, err = json.Marshal(Meta{
		AgentStatus: &AgentStatus{State: "working", At: "t"},
		LastExit:    &LastExit{Code: 0, At: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"agent_status":{"state":"working","at":"t"}`, `"last_exit":{"code":0,"at":"t"}`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("marshaled %s; want it to contain %s", data, want)
		}
	}
}

// agentReportScript prints the working report from the plan's example, waits
// for gate to exist, prints an idle report, then echoes like cat. The gate is
// what lets the test see the first report in host.json before the second
// replaces it.
func agentReportScript(gate string) string {
	return `printf '\033]7501;state=working:app=claude-code:msg=UnVubmluZyB0ZXN0cw==\033\\'; ` +
		`while [ ! -f ` + gate + ` ]; do sleep 0.02; done; ` +
		`printf '\033]7501;state=idle:app=claude-code\033\\'; exec cat`
}

// TestServePublishesAgentStatusInHostJSON is the end-to-end row: a real host
// whose child prints a report, with no client attached (the daemon and the
// dashboard never attach), shows it in host.json, and a later report
// replaces it.
func TestServePublishesAgentStatusInHostJSON(t *testing.T) {
	requireUnixBind(t)
	gate := filepath.Join(t.TempDir(), "gate")
	h := startServe(t, agentReportScript(gate))

	m := awaitMeta(t, h.cfg, "showed the working report", func(m Meta) bool {
		return m.AgentStatus != nil && m.AgentStatus.State == "working"
	})
	if m.AgentStatus.Msg != "Running tests" {
		t.Errorf("agent_status.msg = %q, want %q", m.AgentStatus.Msg, "Running tests")
	}
	if _, err := time.Parse(time.RFC3339, m.AgentStatus.At); err != nil {
		t.Errorf("agent_status.at = %q, want an RFC 3339 time: %v", m.AgentStatus.At, err)
	}
	if m.LastExit != nil {
		t.Errorf("last_exit = %+v before any child ended, want none", m.LastExit)
	}
	t.Logf("host.json agent_status: state=%q msg=%q at=%q", m.AgentStatus.State, m.AgentStatus.Msg, m.AgentStatus.At)

	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m = awaitMeta(t, h.cfg, "showed the idle report", func(m Meta) bool {
		return m.AgentStatus != nil && m.AgentStatus.State == "idle"
	})
	if m.AgentStatus.Msg != "" {
		t.Errorf("agent_status.msg = %q after an idle report with no msg, want none", m.AgentStatus.Msg)
	}
}

// TestRestartPublishesLastExitAndClearsAgentStatus is the restart row: after
// a restart in place, host.json (which survives it) carries the old child's
// exit and no agent status, since the resumed agent has not reported, and the
// exit is in host.log too.
func TestRestartPublishesLastExitAndClearsAgentStatus(t *testing.T) {
	requireUnixBind(t)
	f := newRestartFixture(t)
	f.cfg.Argv = []string{"/bin/sh", "-c", agentReportScript(f.releasePath + ".never")}
	f.writeStatus(t, "executing", restartSessionID)
	compressRestartLadder(t)

	h := startServeWithConfig(t, f.cfg)
	awaitMeta(t, h.cfg, "showed the working report", func(m Meta) bool {
		return m.AgentStatus != nil && m.AgentStatus.State == "working"
	})

	refused, err := RequestRestart(f.cfg.Worktree, f.cfg.Name, time.Second, 20*time.Second)
	if err != nil || refused != "" {
		t.Fatalf("RequestRestart = (%q, %v), want (\"\", nil)", refused, err)
	}
	m := awaitMeta(t, h.cfg, "showed the old child's exit", func(m Meta) bool {
		return m.LastExit != nil
	})
	if m.AgentStatus != nil {
		t.Errorf("agent_status = %+v after the restart, want none until the resumed agent reports", m.AgentStatus)
	}
	if m.LastExit.Code != -1 || m.LastExit.Signal != "SIGINT" {
		t.Errorf("last_exit = %+v, want code -1 and SIGINT (the ladder's first rung)", *m.LastExit)
	}
	if _, err := time.Parse(time.RFC3339, m.LastExit.At); err != nil {
		t.Errorf("last_exit.at = %q, want an RFC 3339 time: %v", m.LastExit.At, err)
	}
	if log := hostLog(t, h.cfg); !strings.Contains(log, "killed by SIGINT") {
		t.Errorf("host.log does not record the old child's exit:\n%s", log)
	}
}
