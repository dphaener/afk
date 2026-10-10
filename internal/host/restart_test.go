package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// restart_test.go drives in-place recovery from both directions, the way
// liveness_test.go does: the signal ladder, the resumed command line and the
// detector's restart baseline are exercised directly with synthetic clocks and
// channels (no process, no PTY), and the whole thing is then run end to end
// through a real Serve against two real children — a first child that
// announces itself, goes silent and ignores SIGINT and SIGTERM, and a fake
// `claude` on PATH that records the argv it was handed.
//
// The fake `claude` is what makes the argv assertion worth anything: the host
// resolves its own resumeArgv through PATH, so argv[0] under test is the
// production one rather than something the test injected.

const (
	// restartOldBanner is the first child's only output; everything after it
	// is silence, which is what gets that child marked wedged.
	restartOldBanner = "WEDGED-CHILD-READY"
	// restartResumedBanner is the resumed child's first output, and it is
	// deliberately withheld until the test releases it — see
	// installFakeClaude.
	restartResumedBanner = "RESUMED-CHILD-READY"
	// restartSessionID is the conversation id the fixture status.json
	// records, and the only value the respawned argv may carry.
	restartSessionID = "11111111-2222-3333-4444-555555555555"
)

// writeStatusFile seeds a status.json with all nine schema keys
// (status-schema.md §2), plan_name matching its containing directory as
// invariant 1 requires. Used by both the pure tests and the fixture, so a
// single place decides what a well-formed status.json looks like here.
func writeStatusFile(t *testing.T, dir, phase, sessionID string) string {
	t.Helper()
	body := fmt.Sprintf(`{
  "session_id": %q,
  "plan_name": %q,
  "phase": %q,
  "branch": "sess",
  "base_branch": "main",
  "loop_count": 3,
  "awaiting_input": false,
  "tmux_window": "",
  "updated_at": "2026-08-19T12:00:00Z"
}
`, sessionID, filepath.Base(dir), phase)
	return writeStatusRaw(t, dir, body)
}

func writeStatusRaw(t *testing.T, dir, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating %s: %v", dir, err)
	}
	path := filepath.Join(dir, "status.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

// TestEscalate pins the signal ladder as a table. The load-bearing rows are
// "ignores everything but SIGKILL" (the incident's child, and the row that
// proves escalation happens at all), "unkillable" (the ladder must return
// rather than wait forever on a child stuck in the kernel), and
// "already gone" (a child reaped between the request and the ladder must not
// be signalled). Every row also asserts the GAPS between signals, because a
// ladder that fires all three at once would satisfy the order assertions
// while giving no child a chance to exit cleanly.
func TestEscalate(t *testing.T) {
	const grace = 60 * time.Millisecond
	ladder := []rung{
		{syscall.SIGINT, grace},
		{syscall.SIGTERM, grace},
		{syscall.SIGKILL, grace},
	}

	cases := []struct {
		name string
		// diesOn is the signal whose delivery kills the child; zero means it
		// never dies, and -1 means it was already gone.
		diesOn   syscall.Signal
		wantSent []syscall.Signal
		wantGone bool
	}{
		{name: "already gone", diesOn: -1, wantSent: nil, wantGone: true},
		{name: "exits on the first signal", diesOn: syscall.SIGINT, wantSent: []syscall.Signal{syscall.SIGINT}, wantGone: true},
		{name: "ignores sigint", diesOn: syscall.SIGTERM, wantSent: []syscall.Signal{syscall.SIGINT, syscall.SIGTERM}, wantGone: true},
		{name: "ignores everything but sigkill", diesOn: syscall.SIGKILL, wantSent: []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL}, wantGone: true},
		{name: "unkillable", diesOn: 0, wantSent: []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL}, wantGone: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exited := make(chan struct{})
			if tc.diesOn == -1 {
				close(exited)
			}
			start := time.Now()
			var at []time.Duration
			sent, gone := escalate(ladder, exited, func(sig syscall.Signal) {
				at = append(at, time.Since(start))
				if sig == tc.diesOn {
					close(exited)
				}
			})

			if gone != tc.wantGone {
				t.Errorf("escalate reported gone = %v, want %v", gone, tc.wantGone)
			}
			if len(sent) != len(tc.wantSent) {
				t.Fatalf("escalate sent %v, want %v", sent, tc.wantSent)
			}
			for i := range sent {
				if sent[i] != tc.wantSent[i] {
					t.Fatalf("escalate sent %v, want %v", sent, tc.wantSent)
				}
			}
			for i := 1; i < len(at); i++ {
				if gap := at[i] - at[i-1]; gap < grace {
					t.Errorf("%v followed %v after only %s; each rung must wait out its %s grace",
						sent[i], sent[i-1], gap, grace)
				}
			}
		})
	}
}

// TestRestartLadderIsGracefulBeforeItIsFatal pins the SHIPPED ladder, which
// nothing else does: TestEscalate builds its own three-rung table and the
// end-to-end test compresses restartLadder before the host starts, so both
// would keep passing if production went straight to SIGKILL — the one thing a
// restart must never do. A child killed outright loses whatever it had not yet
// written, including the terminal state it was in the middle of restoring.
func TestRestartLadderIsGracefulBeforeItIsFatal(t *testing.T) {
	want := []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL}
	if len(restartLadder) != len(want) {
		t.Fatalf("restartLadder = %v, want the %d rungs %v", restartLadder, len(want), want)
	}
	for i, r := range restartLadder {
		if r.sig != want[i] {
			t.Errorf("restartLadder[%d] sends %v, want %v: the child is asked to leave before it is killed", i, r.sig, want[i])
		}
		if r.grace <= 0 {
			t.Errorf("restartLadder[%d] (%v) has a grace of %s, so the next rung fires immediately — the same as never sending this one", i, r.sig, r.grace)
		}
	}
}

// TestResumeArgv pins the respawned command line, per provider, against an id
// read from a fixture status.json. The bans rule out two mistakes invisible in
// a passing dashboard: a prompt argument re-runs a lifecycle that has already
// finished, and --fork-session mints a new session id, orphaning the one
// status.json records and silently killing hook targeting (status-schema.md §7
// matches hooks on session_id equality).
//
// Load-bearing rows: "empty provider" is every host started before providers
// existed and every Claude host today (the spawner omits --provider), so it
// must resume exactly as claude did; "codex" is the reason this test is a
// table, since resuming a codex conversation under claude loses it; "unknown"
// must refuse rather than fall back to claude, because the wrong binary
// cannot resume the conversation either.
func TestResumeArgv(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".afk", "sess")
	path := writeStatusFile(t, dir, "executing", restartSessionID)

	id, err := resumeSessionID(path)
	if err != nil {
		t.Fatalf("resumeSessionID(%s) = %v", path, err)
	}
	if id != restartSessionID {
		t.Fatalf("resumeSessionID = %q, want the fixture's session_id %q", id, restartSessionID)
	}

	cases := []struct {
		name     string
		provider string
		want     []string
		wantErr  bool
	}{
		{name: "claude", provider: "claude", want: []string{"claude", "--resume", restartSessionID}},
		{name: "empty provider resumes as claude", provider: "", want: []string{"claude", "--resume", restartSessionID}},
		{name: "codex", provider: "codex", want: []string{"codex", "resume", restartSessionID}},
		{name: "opencode", provider: "opencode", want: []string{"opencode", "--standalone", "-s", restartSessionID}},
		{name: "unknown provider is refused", provider: "gemini", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resumeArgv(tc.provider, id)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resumeArgv(%q) = %q, want an error", tc.provider, got)
				}
				if !strings.Contains(err.Error(), tc.provider) {
					t.Errorf("resumeArgv(%q) error = %q, want it to name the provider", tc.provider, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("resumeArgv(%q) error = %v", tc.provider, err)
			}
			if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
				t.Fatalf("resumeArgv(%q) = %q, want %q", tc.provider, got, tc.want)
			}
			for _, banned := range []string{"--fork-session", "/afk:go", "--session", "--last"} {
				for _, arg := range got {
					if strings.Contains(arg, banned) {
						t.Errorf("resumeArgv(%q) = %q, which must never carry %q", tc.provider, got, banned)
					}
				}
			}
		})
	}
}

// TestServeRecordsProviderInHostJSON pins host.json's side of the provider at
// Serve's startup write. The empty row is load-bearing: Claude hosts are
// spawned without --provider, and their host.json must stay free of the key
// (omitempty) so it reads exactly as before providers existed.
func TestServeRecordsProviderInHostJSON(t *testing.T) {
	requireUnixBind(t)
	for _, provider := range []string{"", "codex", "opencode"} {
		t.Run("provider="+provider, func(t *testing.T) {
			cfg := serveConfigFor(t, "while true; do sleep 0.05; done")
			cfg.Provider = provider
			h := startServeWithConfig(t, cfg)

			deadline := time.Now().Add(5 * time.Second)
			for {
				meta, err := ReadMeta(cfg.Worktree, cfg.Name)
				if err == nil {
					if meta.Provider != provider {
						t.Errorf("host.json provider = %q, want %q", meta.Provider, provider)
					}
					raw, rerr := os.ReadFile(cfg.MetaPath)
					if rerr != nil {
						t.Fatalf("reading %s: %v", cfg.MetaPath, rerr)
					}
					if has := strings.Contains(string(raw), `"provider"`); has != (provider != "") {
						t.Errorf("host.json carries a provider key = %v, want %v:\n%s", has, provider != "", raw)
					}
					break
				}
				select {
				case <-h.stopped:
					t.Fatalf("host exited before publishing host.json: %v", h.err)
				default:
				}
				if time.Now().After(deadline) {
					t.Fatalf("host never published %s: %v", cfg.MetaPath, err)
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

// TestRestartArgvRefusesAnUnknownProvider drives the refusal through the
// host's own path, the one beginRestart takes before touching the child: a
// valid status.json must not rescue a provider this binary cannot resume.
func TestRestartArgvRefusesAnUnknownProvider(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".afk", "sess")
	writeStatusFile(t, dir, "executing", restartSessionID)

	cases := []struct {
		name     string
		provider string
		want     string
		wantErr  bool
	}{
		{name: "empty", provider: "", want: "claude --resume " + restartSessionID},
		{name: "codex", provider: "codex", want: "codex resume " + restartSessionID},
		{name: "opencode", provider: "opencode", want: "opencode --standalone -s " + restartSessionID},
		{name: "unknown", provider: "nope", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &hostRuntime{cfg: ServeConfig{
				Name:     "sess",
				MetaPath: filepath.Join(dir, "host.json"),
				Provider: tc.provider,
			}}
			got, err := h.restartArgv()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("restartArgv() = %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("restartArgv() error = %v", err)
			}
			if strings.Join(got, " ") != tc.want {
				t.Errorf("restartArgv() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestResumeSessionID covers every way status.json can fail to name a
// conversation. All of them must refuse rather than guess: resuming the wrong
// id joins somebody else's conversation, and resuming none at all starts a
// fresh one on top of finished work.
func TestResumeSessionID(t *testing.T) {
	cases := []struct {
		name string
		// seed writes the fixture and returns the path to read; a nil seed
		// means the file is never created.
		seed   func(t *testing.T, dir string) string
		wantID string
		// wantErr is the substring the refusal must carry. Load-bearing: a
		// stale read that fell through to the empty-session_id check would
		// still refuse, still name the file, and still be wrong about why.
		wantErr string
	}{
		{
			name:   "a well-formed status.json",
			seed:   func(t *testing.T, dir string) string { return writeStatusFile(t, dir, "executing", restartSessionID) },
			wantID: restartSessionID,
		},
		{
			name:    "no status.json at all",
			seed:    func(t *testing.T, dir string) string { return filepath.Join(dir, "status.json") },
			wantErr: "is not usable (missing)",
		},
		{
			name:    "unparseable status.json",
			seed:    func(t *testing.T, dir string) string { return writeStatusRaw(t, dir, "{not json at all") },
			wantErr: "is not usable (unparseable)",
		},
		{
			name: "an unknown key, which the schema calls corruption",
			seed: func(t *testing.T, dir string) string {
				return writeStatusRaw(t, dir, fmt.Sprintf(`{"session_id":%q,"plan_name":"sess","phase":"executing","branch":"sess","base_branch":"main","loop_count":0,"awaiting_input":false,"tmux_window":"","updated_at":"2026-08-19T12:00:00Z","surprise":1}`, restartSessionID))
			},
			wantErr: "is not usable (unknown-field)",
		},
		{
			name:    "an empty session_id",
			seed:    func(t *testing.T, dir string) string { return writeStatusFile(t, dir, "planning", "") },
			wantErr: "records no session_id",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), ".afk", "sess")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("creating %s: %v", dir, err)
			}
			path := tc.seed(t, dir)

			id, err := resumeSessionID(path)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("resumeSessionID = %q, want an error", id)
				}
				if id != "" {
					t.Errorf("resumeSessionID returned %q alongside its error; a failed read must invent nothing", id)
				}
				if !strings.Contains(err.Error(), path) {
					t.Errorf("error %q does not name %s, so an operator cannot tell which file is at fault", err, path)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error %q does not say %q; the refusal has to name the right reason", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resumeSessionID(%s) = %v", path, err)
			}
			if id != tc.wantID {
				t.Errorf("resumeSessionID = %q, want %q", id, tc.wantID)
			}
		})
	}
}

// TestLivenessNoteRestart pins what a restart does to the wedge detector. It
// exists separately from the end-to-end test because that test cannot isolate
// this: the resumed child's first byte would clear the verdict on its own, so
// only a direct test can prove the restart clears it without pretending the
// new child produced output it has not produced.
func TestLivenessNoteRestart(t *testing.T) {
	p := livenessPolicy{
		silence:  30 * time.Second,
		deadline: 5 * time.Second,
		interval: 60 * time.Second,
		tick:     time.Second,
		publish:  5 * time.Minute,
	}
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	l := newLiveness(p, start)

	// An earlier publish, so the force-publish assertion below is not vacuous:
	// with lastPublish still at the zero time the rate limit is satisfied
	// anyway, and removing force would not turn this red (learnings.md W3.T1).
	l.noteOutput(start.Add(time.Second))
	if _, ok := l.publishDue(start.Add(2 * time.Second)); !ok {
		t.Fatalf("the first publish after output should have been due")
	}

	probeAt := start.Add(time.Minute)
	l.noteProbeSent(probeAt)
	wedgedAt := probeAt.Add(p.deadline)
	if d := l.observe(wedgedAt, true); d != probeWedge {
		t.Fatalf("observe past the probe deadline = %v, want %v", d, probeWedge)
	}
	if _, first := l.markWedged(wedgedAt); !first {
		t.Fatalf("markWedged did not report the first verdict of this wedge")
	}
	if _, ok := l.publishDue(wedgedAt); !ok {
		t.Fatalf("a wedge verdict should publish immediately")
	}

	restartAt := wedgedAt.Add(time.Second)
	l.noteRestart(restartAt)

	snap, ok := l.publishDue(restartAt)
	if !ok {
		t.Fatalf("a restart must publish host.json immediately, even inside the %s publish interval", p.publish)
	}
	if snap.WedgedSince != "" {
		t.Errorf("wedged_since = %q after a restart, want it cleared", snap.WedgedSince)
	}
	if snap.LastOutputAt != "" {
		t.Errorf("last_output_at = %q after a restart; the resumed child has produced nothing, so the host must not claim otherwise", snap.LastOutputAt)
	}
	if d, ok := l.takeRecovery(); ok {
		t.Errorf("takeRecovery = (%s, true) after a restart; a restart is not the child answering", d)
	}

	// The silence baseline is the restart, not the child it replaced.
	if d := l.observe(restartAt.Add(p.silence-time.Millisecond), true); d != probeIdle {
		t.Errorf("observe %s after a restart = %v, want %v: the resumed child's silence starts at the restart",
			p.silence-time.Millisecond, d, probeIdle)
	}
	// And the probe rate limit survives it, so a restart cannot be used to
	// shake a child once per tick.
	if d := l.observe(restartAt.Add(p.silence+time.Millisecond), true); d != probeIdle {
		t.Errorf("observe = %v inside the %s probe interval, want %v", d, p.interval, probeIdle)
	}
	if d := l.observe(probeAt.Add(p.interval+time.Second), true); d != probeNudge {
		t.Errorf("observe past the probe interval = %v, want %v", d, probeNudge)
	}
}

// restartFixture is one end-to-end restart test's scaffolding: a session
// directory holding status.json, a first child that wedges, and a fake
// `claude` on PATH standing in for the resumed one.
type restartFixture struct {
	cfg ServeConfig
	// dir is <worktree>/.afk/<name>, where status.json, host.json and
	// host.log all live (internal/session's statusDir).
	dir string
	// oldPIDPath is where the first child records its pid.
	oldPIDPath string
	// resumedPIDPath and resumedArgsPath are what the fake `claude` records:
	// its pid, and the arguments it was handed.
	resumedPIDPath  string
	resumedArgsPath string
	// resumedSizePath is the terminal size the resumed child was born with,
	// as it sees it.
	resumedSizePath string
	// releasePath gates the resumed child's first byte of output; see
	// installFakeClaude.
	releasePath string
	// hushPath is how the first child is told to fall silent, which is what
	// gets it wedged; see wedgedChildScript.
	hushPath string
}

// TestSetResumedChildRecordsBothTheArgvAndTheChildPID pins host.json's side of
// a restart at the source, with no process involved: the resumed child's argv
// AND its pid replace the retired child's in baseMeta, which is what
// publishLiveness copies onto disk, while Meta.Provider survives untouched.
// The row that matters most is the negative one — Meta.PID, the HOST's pid,
// is left alone. internal/discover signal-0s Meta.PID to decide whether a
// session is reachable, so writing a child pid there would make a live
// session look dead the instant a restart succeeded.
func TestSetResumedChildRecordsBothTheArgvAndTheChildPID(t *testing.T) {
	h := &hostRuntime{}
	h.baseMeta = hostMeta{
		Meta: Meta{
			PID:      4242,
			Socket:   "/tmp/afk-test.sock",
			Argv:     []string{"claude", "/afk:go --session x"},
			Provider: "claude",
		},
		ChildPID: 111,
	}

	h.setResumedChild([]string{"claude", "--resume", restartSessionID}, 222)

	if got, want := strings.Join(h.baseMeta.Argv, " "), "claude --resume "+restartSessionID; got != want {
		t.Errorf("baseMeta.Argv = %q, want %q", got, want)
	}
	if h.baseMeta.ChildPID != 222 {
		t.Errorf("baseMeta.ChildPID = %d, want the resumed child's 222", h.baseMeta.ChildPID)
	}
	if h.baseMeta.PID != 4242 {
		t.Errorf("baseMeta.PID = %d, want the host's own 4242 left untouched", h.baseMeta.PID)
	}
	if h.baseMeta.Provider != "claude" {
		t.Errorf("baseMeta.Provider = %q, want %q kept across the restart", h.baseMeta.Provider, "claude")
	}
}

func newRestartFixture(t *testing.T) restartFixture {
	t.Helper()
	requireSh(t)

	repoRoot := t.TempDir()
	name := "sess"
	dir := filepath.Join(repoRoot, ".afk", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating %s: %v", dir, err)
	}

	f := restartFixture{
		dir:             dir,
		oldPIDPath:      filepath.Join(repoRoot, "old.pid"),
		resumedPIDPath:  filepath.Join(repoRoot, "resumed.pid"),
		resumedArgsPath: filepath.Join(repoRoot, "resumed.args"),
		resumedSizePath: filepath.Join(repoRoot, "resumed.size"),
		releasePath:     filepath.Join(repoRoot, "resumed.release"),
		hushPath:        filepath.Join(repoRoot, "old.hush"),
	}
	f.cfg = ServeConfig{
		Name:     name,
		Worktree: repoRoot,
		Socket:   filepath.Join(shortSocketDir(t), "h.sock"),
		MetaPath: filepath.Join(dir, "host.json"),
		LogPath:  filepath.Join(dir, "host.log"),
		Argv:     []string{"/bin/sh", "-c", wedgedChildScript(f.oldPIDPath, f.hushPath)},
	}
	f.installFakeClaude(t)
	return f
}

// wedgedChildScript models the child from the incident: it announces itself
// until told to hush, then says nothing ever again, ignores SIGINT and SIGTERM
// so the restart has to walk the whole ladder, and ignores SIGWINCH by default
// disposition — which is precisely what gets it marked wedged.
//
// The banner repeats rather than being printed once because pumpPTY DISCARDS
// output while nobody is attached (host.go): a single banner printed before
// the test's client attaches would be gone, and the test would be waiting for
// something that had already been thrown away.
func wedgedChildScript(pidPath, hushPath string) string {
	return fmt.Sprintf("trap '' INT TERM\necho $$ > %s\nwhile [ ! -f %s ]; do echo %s; sleep 0.05; done\nwhile true; do sleep 0.05; done\n",
		pidPath, hushPath, restartOldBanner)
}

// installFakeClaude puts an executable named `claude` on PATH so the host's
// own resumeArgv resolves to it. That is the point: the argv under test is the
// production one, argv[0] included, instead of one the test handed in.
// exec.LookPath consults the test process's PATH (cmd.Env is not used for
// resolution), so t.Setenv is the whole mechanism.
//
// The script records its arguments and its pid immediately, then stays SILENT
// until releasePath exists. That gap is load-bearing: "host.json shows no
// wedge after the restart" only says something about the restart while the
// resumed child has produced no output that could have cleared it.
func (f restartFixture) installFakeClaude(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
echo "$@" > %s
stty size > %s 2>/dev/null
echo $$ > %s
while [ ! -f %s ]; do sleep 0.02; done
echo %s
while true; do sleep 0.05; done
`, f.resumedArgsPath, f.resumedSizePath, f.resumedPIDPath, f.releasePath, restartResumedBanner)

	path := filepath.Join(dir, "claude")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("installing the fake claude: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func (f restartFixture) writeStatus(t *testing.T, phase, sessionID string) {
	t.Helper()
	writeStatusFile(t, f.dir, phase, sessionID)
}

// hush tells the first child to stop producing output, which is what starts
// the silence the wedge detector measures.
func (f restartFixture) hush(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(f.hushPath, nil, 0o644); err != nil {
		t.Fatalf("hushing the first child: %v", err)
	}
}

// release lets the resumed child start talking.
func (f restartFixture) release(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(f.releasePath, nil, 0o644); err != nil {
		t.Fatalf("releasing the resumed child: %v", err)
	}
}

// compressRestartLadder shrinks the signal ladder for one test. Like
// compressLivenessTiming it must be called BEFORE the host starts: the serve
// loop reads restartLadder when it handles a Restart frame, and registering
// the restore first is what makes t.Cleanup's LIFO order run it after the
// host has stopped.
func compressRestartLadder(t *testing.T) {
	t.Helper()
	prev := restartLadder
	t.Cleanup(func() { restartLadder = prev })
	restartLadder = []rung{
		{syscall.SIGINT, 60 * time.Millisecond},
		{syscall.SIGTERM, 60 * time.Millisecond},
		{syscall.SIGKILL, time.Second},
	}
}

// compressRestartLiveness is compressLivenessTiming with one deliberate
// difference: a probe interval far longer than the test, so the detector
// wedges the FIRST child but cannot get round to probing the resumed one.
// Without that, "no wedge after the restart" could just mean the second
// verdict had not landed yet.
func compressRestartLiveness(t *testing.T) {
	t.Helper()
	prev := livenessTiming
	t.Cleanup(func() { livenessTiming = prev })
	livenessTiming = livenessPolicy{
		silence:  300 * time.Millisecond,
		deadline: 200 * time.Millisecond,
		interval: time.Hour,
		tick:     20 * time.Millisecond,
		publish:  50 * time.Millisecond,
	}
}

// readHostMeta parses host.json into the host's full on-disk shape. It
// cannot go through ReadMeta the way awaitMeta (liveness_test.go) does,
// because Meta deliberately has no child_pid — that key belongs to hostMeta
// (host.go), and asserting a restart moved it means parsing it back.
func readHostMeta(cfg ServeConfig) (hostMeta, error) {
	data, err := os.ReadFile(cfg.MetaPath)
	if err != nil {
		return hostMeta{}, err
	}
	var m hostMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return hostMeta{}, err
	}
	return m, nil
}

// awaitHostMeta is awaitMeta for hostMeta: it polls host.json until want
// holds, so a restart's republish can be asserted without racing the
// monitor's publish tick.
func awaitHostMeta(t *testing.T, cfg ServeConfig, what string, want func(hostMeta) bool) hostMeta {
	t.Helper()
	deadline := time.Now().Add(testBudget)
	var last hostMeta
	for {
		m, err := readHostMeta(cfg)
		if err == nil {
			last = m
			if want(m) {
				return m
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("host.json never %s; last read: %+v", what, last)
		}
		time.Sleep(testPollStep)
	}
}

// awaitDead polls until pid is gone, which is the only honest way to assert a
// kill landed: Alive is advisory (see meta.go) but a pid that stops answering
// signal 0 within the budget did exit.
func awaitDead(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(testBudget)
	for time.Now().Before(deadline) {
		if !Alive(pid) {
			return
		}
		time.Sleep(testPollStep)
	}
	t.Fatalf("child %d was still alive %s after the restart", pid, testBudget)
}

// TestRestartResumesTheWedgedChildInPlace is the feature end to end: a wedged
// child, a Restart frame, and afterwards a different child running
// `claude --resume <id>` on the same host, the same socket and the same
// attached client.
//
// Two of its assertions are about host.json's two pids at once, and they are
// the load-bearing pair: child_pid must move from the wedged child to the
// resumed one, while pid must NOT move, because pid is the host's and the host
// is what survives (internal/discover signal-0s it to decide HostRunning).
func TestRestartResumesTheWedgedChildInPlace(t *testing.T) {
	requireUnixBind(t)

	f := newRestartFixture(t)
	f.writeStatus(t, "executing", restartSessionID)
	compressRestartLiveness(t)
	compressRestartLadder(t)

	h := startServeWithConfig(t, f.cfg)
	oldPID := readPIDFile(t, f.oldPIDPath)

	// host.json names the child it is hosting from the moment Serve writes
	// it. Asserted before the restart so that the post-restart assertion is
	// about a value that CHANGED, not one that appeared for the first time.
	awaitHostMeta(t, f.cfg, "recorded the first child's pid", func(m hostMeta) bool { return m.ChildPID == oldPID })

	conn := h.dial(t)
	buf := drainClient(t, conn)
	awaitBuffer(t, buf, restartOldBanner)
	// A real terminal size, so the resumed child inherits the client's
	// geometry rather than the 24x80 startup default.
	sendFrame(t, conn, Resize, EncodeResize(40, 100))
	f.hush(t)
	awaitMeta(t, f.cfg, "marked the silent child wedged", func(m Meta) bool { return m.WedgedSince != "" })

	sendFrame(t, conn, Restart, nil)

	resumedPID := readPIDFile(t, f.resumedPIDPath)
	if resumedPID == oldPID {
		t.Fatalf("the resumed child has the old pid %d; nothing was actually restarted", oldPID)
	}
	awaitDead(t, oldPID)

	if got, want := waitForFile(t, f.resumedArgsPath), "--resume "+restartSessionID; got != want {
		t.Errorf("the resumed child was run with %q, want exactly %q", got, want)
	}
	// The resumed child is on a live PTY sized to the attached client, not to
	// the 24-row startup default. Rows only: the repaint nudge moves the
	// column count and puts it straight back, so the child can legitimately
	// observe either value for cols.
	if size := strings.Fields(waitForFile(t, f.resumedSizePath)); len(size) == 0 || size[0] != "40" {
		t.Errorf("the resumed child sees a terminal of %q, want the attached client's 40 rows", size)
	}

	// host.json, asserted while the resumed child has still produced nothing:
	// neither the cleared wedge nor the new argv can have come from its
	// output.
	m := awaitHostMeta(t, f.cfg, "published the resumed child's pid and argv with no wedge", func(m hostMeta) bool {
		return m.WedgedSince == "" && len(m.Argv) == 3 && m.Argv[1] == "--resume" && m.ChildPID == resumedPID
	})
	if m.ChildPID != resumedPID {
		t.Errorf("host.json child_pid = %d, want the resumed child's %d", m.ChildPID, resumedPID)
	}
	if got, want := strings.Join(m.Argv, " "), "claude --resume "+restartSessionID; got != want {
		t.Errorf("host.json argv = %q, want %q", got, want)
	}
	if m.LastOutputAt != "" {
		t.Errorf("host.json last_output_at = %q, but the resumed child has not written anything yet", m.LastOutputAt)
	}
	if m.PID != os.Getpid() {
		t.Errorf("host.json pid = %d, want the live host's %d: a restart must not change which process owns the session", m.PID, os.Getpid())
	}

	// The same client, still attached, now sees the resumed child.
	f.release(t)
	awaitBuffer(t, buf, restartResumedBanner)

	if h.returned() {
		t.Fatalf("Serve returned across the restart: %v", h.err)
	}

	logText := hostLog(t, f.cfg)
	for _, want := range []string{
		"restart: sending interrupt",
		"restart: sending terminated",
		"restart: sending killed",
	} {
		if !strings.Contains(logText, want) {
			t.Errorf("host.log never recorded %q; the ladder did not escalate:\n%s", want, logText)
		}
	}
	killed := strings.Index(logText, "restart: sending killed")
	if nudged := strings.LastIndex(logText, "nudged pty to force a repaint"); nudged < killed {
		t.Errorf("host.log has no repaint nudge after the restart, so an attached pane would keep showing the dead child's screen:\n%s", logText)
	}
	if strings.Contains(logText, "--fork-session") {
		t.Errorf("host.log mentions --fork-session, which would orphan status.json's session_id:\n%s", logText)
	}

	// The socket is still there. Last, because dialing steals the session
	// from the client the assertions above depend on.
	c, err := net.Dial("unix", f.cfg.Socket)
	if err != nil {
		t.Fatalf("the socket stopped accepting connections across the restart: %v", err)
	}
	_ = c.Close()
}

// TestRestartWithoutAUsableSessionIDLeavesTheSessionAlone covers every way
// status.json can fail to name a conversation. Each row must leave the
// session exactly as it was: the whole point of reading the id BEFORE
// touching the child is that a session whose conversation cannot be found is
// still a session, and killing its child would destroy the only copy of it.
func TestRestartWithoutAUsableSessionIDLeavesTheSessionAlone(t *testing.T) {
	requireUnixBind(t)

	cases := []struct {
		name    string
		seed    func(t *testing.T, f restartFixture)
		wantMsg string
	}{
		{
			name:    "no status.json at all",
			seed:    func(t *testing.T, f restartFixture) {},
			wantMsg: "is not usable (missing)",
		},
		{
			name:    "an empty session_id",
			seed:    func(t *testing.T, f restartFixture) { f.writeStatus(t, "planning", "") },
			wantMsg: "records no session_id",
		},
		{
			name:    "unparseable status.json",
			seed:    func(t *testing.T, f restartFixture) { writeStatusRaw(t, f.dir, "{not json at all") },
			wantMsg: "is not usable (unparseable)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRestartFixture(t)
			tc.seed(t, f)
			compressRestartLadder(t)

			h := startServeWithConfig(t, f.cfg)
			pid := readPIDFile(t, f.oldPIDPath)

			conn := h.dial(t)
			buf := drainClient(t, conn)
			awaitBuffer(t, buf, restartOldBanner)
			sendFrame(t, conn, Restart, nil)

			awaitBuffer(t, buf, "restart failed")
			awaitBuffer(t, buf, tc.wantMsg)
			awaitLogLine(t, f.cfg.LogPath, "refusing to restart")

			if !Alive(pid) {
				t.Fatalf("the restart killed the child (pid %d) even though it could not find a conversation to resume", pid)
			}
			if _, err := os.Stat(f.resumedArgsPath); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("a child was resumed anyway: %s exists (stat err %v)", f.resumedArgsPath, err)
			}
			if got, want := strings.Join(readMetaNow(t, f.cfg).Argv, " "), strings.Join(f.cfg.Argv, " "); got != want {
				t.Errorf("host.json argv = %q, want the original %q", got, want)
			}
			if h.returned() {
				t.Fatalf("Serve returned after a refused restart: %v", h.err)
			}
		})
	}
}

// slowRestartLadder installs a ladder whose graceful rungs are long enough to
// observe the serve loop MID-restart (the fixture child traps INT and TERM, so
// both graces are spent in full) while SIGKILL still ends it promptly. Total
// ladder time is ~3s; the responsiveness assertions below demand answers well
// inside the first rung.
func slowRestartLadder(t *testing.T) time.Duration {
	t.Helper()
	prev := restartLadder
	t.Cleanup(func() { restartLadder = prev })
	restartLadder = []rung{
		{syscall.SIGINT, 1500 * time.Millisecond},
		{syscall.SIGTERM, 1500 * time.Millisecond},
		{syscall.SIGKILL, time.Second},
	}
	return 3 * time.Second
}

// TestServeLoopStaysResponsiveWhileTheRestartLadderRuns is fix cycle 2's
// answer to the finding that restartChild ran inline on the single serve
// loop: with the ladder's graces (~9s shipped) spent on that goroutine while
// h.ctrl stayed a blocking depth-16 channel, a control-frame storm during a
// slow restart parked readClient, the socket stopped being drained, and the
// attached pane's own 5s write deadline closed its connection — a bounded
// echo of the very freeze-cascade this plan exists to remove.
//
// The test reproduces exactly that storm: a Restart against a child that
// ignores SIGINT and SIGTERM (so the ladder runs its graces in full), then
// more control frames than h.ctrl can buffer, then a Detach. Two assertions
// pin the fix, each catching what the other cannot:
//
//   - the Detach must take effect (the host closes the connection) while the
//     ladder is still inside its first rung — against the pre-fix code the
//     loop is parked in restartChild, readClient is parked on the full ctrl
//     channel, and the EOF only arrives after the whole ladder has run;
//   - host.log must record the detach BEFORE it records the child reaped,
//     which is a timing-free statement of the same fact.
//
// The restart itself must still complete after the client bailed: the ladder
// and respawn belong to the host, not to whoever asked for them.
func TestServeLoopStaysResponsiveWhileTheRestartLadderRuns(t *testing.T) {
	requireUnixBind(t)

	f := newRestartFixture(t)
	f.writeStatus(t, "executing", restartSessionID)
	ladderTotal := slowRestartLadder(t)

	h := startServeWithConfig(t, f.cfg)
	oldPID := readPIDFile(t, f.oldPIDPath)

	conn := h.dial(t)
	buf := &syncBuffer{}
	eof := make(chan struct{})
	go func() {
		defer close(eof)
		for {
			typ, payload, err := ReadFrame(conn)
			if err != nil {
				return
			}
			if typ == Data {
				_, _ = buf.Write(payload)
			}
		}
	}()
	t.Cleanup(func() {
		_ = conn.Close()
		<-eof
	})
	awaitBuffer(t, buf, restartOldBanner)

	sendFrame(t, conn, Restart, nil)
	// The storm: twice as many control frames as h.ctrl can buffer. Every one
	// of them must be consumed while the ladder runs, or readClient parks and
	// the socket stops being read at all.
	for i := 0; i < 2*ctrlQueueDepth; i++ {
		sendFrame(t, conn, Resize, EncodeResize(40, uint16(100+i%4)))
	}
	start := time.Now()
	sendFrame(t, conn, Detach, nil)

	responseBudget := ladderTotal / 3
	select {
	case <-eof:
		if elapsed := time.Since(start); elapsed > responseBudget {
			t.Errorf("the host took %s to act on a Detach sent during a restart, want well under %s: the serve loop was parked behind the signal ladder", elapsed.Round(time.Millisecond), responseBudget)
		}
	case <-time.After(ladderTotal + testBudget):
		t.Fatalf("the host never closed the connection after a Detach sent during a restart")
	}

	// The restart the client walked away from still finishes.
	if got, want := waitForFile(t, f.resumedArgsPath), "--resume "+restartSessionID; got != want {
		t.Errorf("the resumed child was run with %q, want %q", got, want)
	}
	awaitDead(t, oldPID)
	if h.returned() {
		t.Fatalf("Serve returned during a restart a client detached from: %v", h.err)
	}

	logText := hostLog(t, f.cfg)
	detached := strings.Index(logText, "client requested detach")
	reaped := strings.Index(logText, "is gone after")
	if detached == -1 || reaped == -1 {
		t.Fatalf("host.log is missing the detach (%d) or the reap (%d):\n%s", detached, reaped, logText)
	}
	if detached > reaped {
		t.Errorf("host.log records the detach only AFTER the child was reaped, so the serve loop sat on it for the whole ladder:\n%s", logText)
	}
}

// awaitRestartResult reads frames off conn until a RestartResult arrives,
// discarding Data (child output and the host's human-readable notify lines
// travel as Data on this same wire) and failing on anything else.
func awaitRestartResult(t *testing.T, conn net.Conn) []byte {
	t.Helper()
	deadline := time.Now().Add(testBudget)
	for {
		if err := conn.SetReadDeadline(deadline); err != nil {
			t.Fatalf("SetReadDeadline: %v", err)
		}
		typ, payload, err := ReadFrame(conn)
		if err != nil {
			t.Fatalf("reading frames while waiting for a RestartResult: %v", err)
		}
		switch typ {
		case RestartResult:
			return payload
		case Data:
		default:
			t.Fatalf("got a %v frame while waiting for a RestartResult", typ)
		}
	}
}

// TestRestartReportsItsOutcomeAsARestartResultFrame pins the typed outcome
// channel the dashboard's dial-and-send trigger depends on (fix cycle 2's
// blocker): a client that requests a restart must be able to LEARN how it
// ended, not infer it from silence. A refusal carries the reason as the
// payload; a successful respawn carries an empty payload. The human-readable
// "[afk] restart failed" Data line is a rendering for panes, not a protocol,
// which is why the frame exists in addition to it.
func TestRestartReportsItsOutcomeAsARestartResultFrame(t *testing.T) {
	requireUnixBind(t)

	t.Run("a refusal carries the reason", func(t *testing.T) {
		f := newRestartFixture(t)
		// No status.json at all: the exact refusal W4.T1 defines.
		compressRestartLadder(t)

		h := startServeWithConfig(t, f.cfg)
		pid := readPIDFile(t, f.oldPIDPath)

		conn := h.dial(t)
		sendFrame(t, conn, Restart, nil)

		payload := awaitRestartResult(t, conn)
		if len(payload) == 0 {
			t.Fatalf("a refused restart produced an empty RestartResult, which reads as success")
		}
		if got := string(payload); !strings.Contains(got, "not usable") {
			t.Errorf("RestartResult payload = %q, want the refusal reason (status.json not usable)", got)
		}
		if !Alive(pid) {
			t.Fatalf("the refused restart killed the child (pid %d)", pid)
		}
	})

	t.Run("a successful respawn carries an empty payload", func(t *testing.T) {
		f := newRestartFixture(t)
		f.writeStatus(t, "executing", restartSessionID)
		compressRestartLadder(t)

		h := startServeWithConfig(t, f.cfg)
		oldPID := readPIDFile(t, f.oldPIDPath)

		conn := h.dial(t)
		sendFrame(t, conn, Restart, nil)

		if payload := awaitRestartResult(t, conn); len(payload) != 0 {
			t.Fatalf("a successful restart produced RestartResult %q, want an empty payload", payload)
		}
		awaitDead(t, oldPID)
		if got, want := waitForFile(t, f.resumedArgsPath), "--resume "+restartSessionID; got != want {
			t.Errorf("the resumed child was run with %q, want %q", got, want)
		}
	})
}
