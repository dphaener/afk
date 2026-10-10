package host

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// This file exercises the wedge detector from two directions: decideProbe and
// the liveness tracker are driven directly with synthetic clocks (no PTY, no
// process), and the end-to-end cases are driven through a real Serve against
// two children — the W1.T1 fixture, which can be told mid-run to stop
// producing output, and probeChild below, which is silent forever but
// repaints on SIGWINCH. The pair is the whole point of the feature: silence
// alone must never be a verdict, and only the nudge tells the two apart.

const (
	// probeChildRoleEnv, set to probeChildRoleValue, makes this test binary
	// behave as the silent-but-responsive child instead of running the
	// suite. Named distinctly from lifecycle_test.go's AFK_TEST_HOST_ROLE
	// and stuckchild_test.go's AFK_TEST_STUCKCHILD_ROLE so the three
	// re-exec dispatches can never collide.
	probeChildRoleEnv   = "AFK_TEST_PROBECHILD_ROLE"
	probeChildRoleValue = "probechild"
	// probeChildStdinLogEnv names a file the child appends every byte it
	// reads from its stdin to. The probe must never write to the child's
	// stdin, so this file staying empty is the assertion.
	probeChildStdinLogEnv = "AFK_TEST_PROBECHILD_STDINLOG"

	probeChildBanner = "PROBECHILD-READY"
	probeChildWinch  = "PROBECHILD-WINCH"
)

// init intercepts a re-exec into the probe-child role before go test's
// generated main() ever calls TestMain (declared in lifecycle_test.go for its
// own unrelated role), the same way stuckchild_test.go does — see its package
// comment for why this has to be init rather than a second TestMain.
func init() {
	if os.Getenv(probeChildRoleEnv) == probeChildRoleValue {
		os.Exit(probeChildMain())
	}
}

// probeChildMain models the child the detector must NOT flag: a healthy TUI
// idling at a prompt. It says nothing at all on its own — the case that makes
// output silence useless as a verdict — but it repaints on every SIGWINCH,
// which is the one thing the host's probe asks of it.
//
// Everything it reads from stdin is appended to probeChildStdinLogEnv's path,
// so a test can prove the probe injected nothing into the child's input.
func probeChildMain() int {
	winch := make(chan os.Signal, 16)
	signal.Notify(winch, syscall.SIGWINCH)
	go func() {
		for range winch {
			if _, err := fmt.Fprintln(os.Stdout, probeChildWinch); err != nil {
				return
			}
		}
	}()

	if _, err := fmt.Fprintln(os.Stdout, probeChildBanner); err != nil {
		return 1
	}

	logPath := os.Getenv(probeChildStdinLogEnv)
	buf := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 && logPath != "" {
			f, ferr := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if ferr != nil {
				return 1
			}
			_, _ = f.Write(buf[:n])
			_ = f.Close()
		}
		if err != nil {
			// EOF once the host closes the PTY master; nothing useful is
			// left to do with this fd either way.
			return 0
		}
	}
}

// probeChildServeConfig builds a ServeConfig whose child is probeChildMain,
// and returns the path its stdin bytes are recorded to. Its role and log
// variables travel in the environment for the same reason
// stuckChildServeConfig's do: startChild hands the child
// childEnv(os.Environ()), so t.Setenv is the only way in.
func probeChildServeConfig(t *testing.T) (ServeConfig, string) {
	t.Helper()

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	repoRoot := t.TempDir()
	stdinLog := filepath.Join(repoRoot, "child-stdin.log")
	t.Setenv(probeChildRoleEnv, probeChildRoleValue)
	t.Setenv(probeChildStdinLogEnv, stdinLog)

	name := "probe"
	return ServeConfig{
		Name:     name,
		Worktree: repoRoot,
		Socket:   filepath.Join(shortSocketDir(t), "h.sock"),
		MetaPath: filepath.Join(repoRoot, ".afk", name, "host.json"),
		LogPath:  filepath.Join(repoRoot, ".afk", name, "host.log"),
		// -test.run=^$ is the same recursion guard stuckchild_test.go uses:
		// if the init dispatch above ever broke, this re-exec runs no tests
		// rather than the whole suite recursively.
		Argv: []string{self, "-test.run=^$"},
	}, stdinLog
}

// compressLivenessTiming shrinks the detector's schedule for one test and
// returns the compressed policy. It must be called BEFORE the host under test
// is started, for two reasons: newHostRuntime copies these values once, so the
// monitor goroutine never reads this variable and the restore registered here
// cannot race it; and t.Cleanup is LIFO, so registering the restore first is
// what makes it run after the host has stopped.
func compressLivenessTiming(t *testing.T) livenessPolicy {
	t.Helper()
	prev := livenessTiming
	t.Cleanup(func() { livenessTiming = prev })
	livenessTiming = livenessPolicy{
		silence:  500 * time.Millisecond,
		deadline: 300 * time.Millisecond,
		interval: time.Second,
		tick:     25 * time.Millisecond,
		publish:  50 * time.Millisecond,
	}
	return livenessTiming
}

// probeCycle is how long one silence-plus-deadline round takes under a
// compressed policy: how long a test must wait before "not wedged" means
// anything.
func probeCycle(p livenessPolicy) time.Duration {
	return p.silence + p.deadline + p.interval
}

// drainClient keeps reading conn for the rest of the test, accumulating Data
// payloads. Every test here needs it: a client that stops reading is dropped
// by the host after clientWriteTimeout, and these tests depend on a client
// staying attached (an unattached session is never probed).
func drainClient(t *testing.T, conn net.Conn) *syncBuffer {
	t.Helper()
	// Clear any deadline a previous helper left behind, so this outlives it.
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	buf := &syncBuffer{}
	done := make(chan struct{})
	go func() {
		defer close(done)
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
		<-done
	})
	return buf
}

// awaitBuffer polls a drained client's output until want appears.
func awaitBuffer(t *testing.T, buf *syncBuffer, want string) {
	t.Helper()
	if !bufferHas(buf, want, testBudget) {
		t.Fatalf("the client never received %q (got %q)", want, buf.String())
	}
}

func bufferHas(buf *syncBuffer, want string, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), want) {
			return true
		}
		time.Sleep(testPollStep)
	}
	return strings.Contains(buf.String(), want)
}

// awaitMeta polls host.json until want holds, which is how these tests read
// the published liveness fields: host.json is the host's only channel for
// them, so asserting on it is asserting on the contract.
func awaitMeta(t *testing.T, cfg ServeConfig, what string, want func(Meta) bool) Meta {
	t.Helper()
	deadline := time.Now().Add(testBudget)
	var last Meta
	for {
		m, err := ReadMeta(cfg.Worktree, cfg.Name)
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

func readMetaNow(t *testing.T, cfg ServeConfig) Meta {
	t.Helper()
	m, err := ReadMeta(cfg.Worktree, cfg.Name)
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}
	return m
}

func hostLog(t *testing.T, cfg ServeConfig) string {
	t.Helper()
	data, err := os.ReadFile(cfg.LogPath)
	if err != nil {
		t.Fatalf("reading host log: %v", err)
	}
	return string(data)
}

// resizeUntilRepaint sends real (size-changing) resizes until the child
// answers with want. Real changes, never a same-size nudge, so "nudged pty to
// force a repaint" in host.log can only have come from the liveness probe.
// The retry exists because a child that has not yet installed its SIGWINCH
// handler silently discards the signal.
func resizeUntilRepaint(t *testing.T, conn net.Conn, buf *syncBuffer, want string) (rows, cols uint16) {
	t.Helper()
	rows, cols = 40, 120
	for attempt := 0; attempt < 20; attempt++ {
		cols = 120 + uint16(attempt%2)
		sendFrame(t, conn, Resize, EncodeResize(rows, cols))
		if bufferHas(buf, want, 250*time.Millisecond) {
			return rows, cols
		}
	}
	t.Fatalf("the child never repainted with %q in response to a resize (got %q)", want, buf.String())
	return 0, 0
}

// TestDecideProbe pins the detector's policy as a table, the way new_test.go
// does. The load-bearing rows are the unattached ones (a session with nobody
// watching must never be probed, however long it has been quiet — that is
// what keeps the host from shaking every idle session in the dashboard), the
// rate-limit row (an attached idle session is nudged at most once per
// interval), and the two deadline rows (a probe still inside its deadline is
// not yet a verdict; one past it is).
func TestDecideProbe(t *testing.T) {
	p := livenessPolicy{
		silence:  30 * time.Second,
		deadline: 5 * time.Second,
		interval: 60 * time.Second,
		tick:     time.Second,
		publish:  5 * time.Second,
	}
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }

	tests := []struct {
		name string
		in   probeInput
		want probeDecision
	}{
		{
			name: "attached and recently noisy",
			in:   probeInput{attached: true, silentSince: ago(time.Second)},
			want: probeIdle,
		},
		{
			name: "attached and silent past the threshold",
			in:   probeInput{attached: true, silentSince: ago(31 * time.Second)},
			want: probeNudge,
		},
		{
			name: "attached and silent exactly at the threshold",
			in:   probeInput{attached: true, silentSince: ago(30 * time.Second)},
			want: probeNudge,
		},
		{
			name: "unattached and silent for hours",
			in:   probeInput{attached: false, silentSince: ago(3 * time.Hour)},
			want: probeIdle,
		},
		{
			name: "silent again but inside the probe interval",
			in:   probeInput{attached: true, silentSince: ago(31 * time.Second), probedAt: ago(40 * time.Second)},
			want: probeIdle,
		},
		{
			name: "silent again after the probe interval",
			in:   probeInput{attached: true, silentSince: ago(31 * time.Second), probedAt: ago(61 * time.Second)},
			want: probeNudge,
		},
		{
			name: "probe still inside its deadline",
			in:   probeInput{attached: true, silentSince: ago(34 * time.Second), probedAt: ago(4 * time.Second), probing: true},
			want: probeIdle,
		},
		{
			name: "probe unanswered at the deadline",
			in:   probeInput{attached: true, silentSince: ago(35 * time.Second), probedAt: ago(5 * time.Second), probing: true},
			want: probeWedge,
		},
		{
			name: "probe unanswered but the client detached",
			in:   probeInput{attached: false, silentSince: ago(35 * time.Second), probedAt: ago(5 * time.Second), probing: true},
			want: probeIdle,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := tt.in
			in.now = now
			if got := decideProbe(in, p); got != tt.want {
				t.Errorf("decideProbe(%+v) = %v, want %v", in, got, tt.want)
			}
		})
	}
}

// TestLivenessTrackerLifecycle walks the tracker through the whole state
// machine on a synthetic clock: silence, nudge, verdict, recovery. The
// load-bearing assertions are that wedged_since keeps its FIRST value across
// a re-probe (a dashboard showing the wedge's age must not see it reset to
// zero every interval), that any output clears it, and that a detached client
// abandons an in-flight probe rather than letting its deadline expire
// unobserved.
func TestLivenessTrackerLifecycle(t *testing.T) {
	// A publish interval longer than the whole silence-plus-deadline round
	// on purpose: it is what makes the immediate publishes below meaningful.
	// With the shipped 5s the interval has always elapsed by the time a
	// verdict lands, and an assertion that a transition publishes at once
	// would pass whether or not it actually bypasses the rate limit.
	p := livenessPolicy{
		silence:  30 * time.Second,
		deadline: 5 * time.Second,
		interval: 60 * time.Second,
		tick:     time.Second,
		publish:  5 * time.Minute,
	}
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	l := newLiveness(p, start)

	// Silence is measured from the host's start until the child says
	// something, so a child that never writes is still probeable.
	if got := l.observe(start.Add(31*time.Second), true); got != probeNudge {
		t.Fatalf("observe on a child that has never written = %v, want %v", got, probeNudge)
	}
	probeAt := start.Add(31 * time.Second)
	l.noteProbeSent(probeAt)

	if got := l.observe(probeAt.Add(4*time.Second), true); got != probeIdle {
		t.Fatalf("observe inside the probe deadline = %v, want %v", got, probeIdle)
	}
	if got := l.observe(probeAt.Add(6*time.Second), true); got != probeWedge {
		t.Fatalf("observe past the probe deadline = %v, want %v", got, probeWedge)
	}

	wedgedAt := probeAt.Add(6 * time.Second)
	silentFor, first := l.markWedged(wedgedAt)
	if !first {
		t.Fatal("markWedged on a healthy session should report the verdict as new")
	}
	if silentFor != wedgedAt.Sub(start) {
		t.Errorf("markWedged reported %s of silence, want %s", silentFor, wedgedAt.Sub(start))
	}
	snap, ok := l.publishDue(wedgedAt)
	if !ok {
		t.Fatal("a new wedge verdict must publish immediately, not wait for the publish interval")
	}
	if snap.WedgedSince != wedgedAt.UTC().Format(time.RFC3339) {
		t.Errorf("published wedged_since = %q, want %q", snap.WedgedSince, wedgedAt.UTC().Format(time.RFC3339))
	}
	if snap.LastOutputAt != "" {
		t.Errorf("published last_output_at = %q, want empty for a child that has never written", snap.LastOutputAt)
	}

	// A second verdict on an already-wedged session must not move
	// wedged_since: the age of the wedge is what makes it actionable.
	reprobe := wedgedAt.Add(2 * time.Minute)
	l.noteProbeSent(reprobe)
	if got := l.observe(reprobe.Add(6*time.Second), true); got != probeWedge {
		t.Fatalf("observe after an unanswered re-probe = %v, want %v", got, probeWedge)
	}
	if _, first := l.markWedged(reprobe.Add(6 * time.Second)); first {
		t.Error("markWedged on an already-wedged session reported the verdict as new")
	}
	if _, ok := l.publishDue(reprobe.Add(7 * time.Second)); ok {
		t.Error("publishDue rewrote host.json for a verdict that had not changed")
	}

	// Output clears the verdict immediately, and the recovery is measured
	// from the FIRST verdict — the assertion that wedged_since was pinned.
	spoke := reprobe.Add(30 * time.Second)
	l.noteOutput(spoke)
	if d, ok := l.takeRecovery(); !ok || d != spoke.Sub(wedgedAt) {
		t.Errorf("takeRecovery = (%s, %v), want (%s, true)", d, ok, spoke.Sub(wedgedAt))
	}
	if _, ok := l.takeRecovery(); ok {
		t.Error("takeRecovery reported the same recovery twice")
	}
	snap, ok = l.publishDue(spoke)
	if !ok {
		t.Fatal("clearing a wedge must publish immediately")
	}
	if snap.WedgedSince != "" {
		t.Errorf("published wedged_since = %q after output resumed, want empty", snap.WedgedSince)
	}
	if snap.LastOutputAt != spoke.UTC().Format(time.RFC3339) {
		t.Errorf("published last_output_at = %q, want %q", snap.LastOutputAt, spoke.UTC().Format(time.RFC3339))
	}

	// Republishing an unchanged, healthy session is rate-limited.
	quiet := spoke.Add(time.Second)
	if _, ok := l.publishDue(quiet); ok {
		t.Error("publishDue republished host.json with nothing changed")
	}
	l.noteOutput(quiet)
	if _, ok := l.publishDue(quiet); ok {
		t.Error("publishDue republished inside the publish interval")
	}

	// A verdict is never held back by that rate limit. This one lands well
	// inside the interval, and a dashboard that only learned of it when the
	// interval ended would show a healthy session for minutes after the host
	// knew better.
	reprobed := quiet.Add(31 * time.Second)
	l.noteProbeSent(reprobed)
	wedgedTwice := reprobed.Add(6 * time.Second)
	if got := l.observe(wedgedTwice, true); got != probeWedge {
		t.Fatalf("observe past a second probe's deadline = %v, want %v", got, probeWedge)
	}
	if _, first := l.markWedged(wedgedTwice); !first {
		t.Error("markWedged after a recovery reported the verdict as a repeat")
	}
	snap, ok = l.publishDue(wedgedTwice)
	if !ok {
		t.Fatal("a wedge verdict must publish immediately, even inside the publish interval")
	}
	if snap.WedgedSince != wedgedTwice.UTC().Format(time.RFC3339) {
		t.Errorf("published wedged_since = %q, want the second verdict's time %q", snap.WedgedSince, wedgedTwice.UTC().Format(time.RFC3339))
	}

	// Only a plain last_output_at change waits for the interval.
	back := wedgedTwice.Add(10 * time.Second)
	l.noteOutput(back)
	if d, ok := l.takeRecovery(); !ok || d != back.Sub(wedgedTwice) {
		t.Errorf("takeRecovery after the second wedge = (%s, %v), want (%s, true)", d, ok, back.Sub(wedgedTwice))
	}
	if _, ok := l.publishDue(back); !ok {
		t.Fatal("clearing the second wedge must publish immediately")
	}
	l.noteOutput(back.Add(time.Second))
	if _, ok := l.publishDue(back.Add(time.Second)); ok {
		t.Error("publishDue republished inside the publish interval")
	}
	if _, ok := l.publishDue(back.Add(6 * time.Minute)); !ok {
		t.Error("publishDue never republished a changed last_output_at after the publish interval")
	}

	// A probe in flight when the last client goes away is abandoned: the
	// deadline it would have expired against passed with nobody watching.
	stale := back.Add(7 * time.Minute)
	l.noteProbeSent(stale)
	if got := l.observe(stale.Add(time.Minute), false); got != probeIdle {
		t.Fatalf("observe with no client attached = %v, want %v", got, probeIdle)
	}
	if got := l.observe(stale.Add(2*time.Minute), true); got != probeNudge {
		t.Errorf("observe after reattaching = %v, want a fresh %v rather than a stale verdict", got, probeNudge)
	}
}

// TestLivenessLeavesAChattyChildAlone is case (1): a child producing output
// never reaches the silence threshold, so it is never probed and never
// wedged. Asserting the host.log has no nudge line at all is what makes this
// meaningful — a detector that probed healthy sessions would shake every
// working session in the dashboard on a timer.
func TestLivenessLeavesAChattyChildAlone(t *testing.T) {
	p := compressLivenessTiming(t)
	cfg, _ := stuckChildServeConfig(t)
	h := startServeWithConfig(t, cfg)

	conn := h.dial(t)
	out := drainClient(t, conn)
	sendFrame(t, conn, Resize, EncodeResize(40, 120))
	awaitBuffer(t, out, stuckChildHeartbeatMarker)

	time.Sleep(3 * probeCycle(p))

	m := awaitMeta(t, cfg, "published last_output_at for a chatty child", func(m Meta) bool {
		return m.LastOutputAt != ""
	})
	if m.WedgedSince != "" {
		t.Errorf("wedged_since = %q for a child that never stopped writing, want empty", m.WedgedSince)
	}
	if got := hostLog(t, cfg); strings.Contains(got, "nudged pty to force a repaint") {
		t.Errorf("the host probed a chatty child; log:\n%s", got)
	}
	if h.returned() {
		t.Fatalf("Serve returned while the child was healthy: %v", h.err)
	}
}

// TestLivenessLeavesASilentButResponsiveChildAlone is case (2), and the
// reason silence alone can never be the verdict: this child says nothing for
// the whole test — exactly what a healthy claude waiting at a prompt does —
// and must not be reported wedged, because it repaints every time it is
// nudged. The nudge line in host.log is load-bearing in the other direction:
// without it the test would prove nothing but that the threshold is longer
// than the test.
func TestLivenessLeavesASilentButResponsiveChildAlone(t *testing.T) {
	p := compressLivenessTiming(t)
	cfg, _ := probeChildServeConfig(t)
	h := startServeWithConfig(t, cfg)

	conn := h.dial(t)
	out := drainClient(t, conn)
	resizeUntilRepaint(t, conn, out, probeChildWinch)

	before := strings.Count(out.String(), probeChildWinch)
	time.Sleep(3 * probeCycle(p))

	if got := hostLog(t, cfg); !strings.Contains(got, "nudged pty to force a repaint") {
		t.Fatalf("the host never probed a silent child, so this test proves nothing; log:\n%s", got)
	}
	if after := strings.Count(out.String(), probeChildWinch); after <= before {
		t.Errorf("the child repainted %d times, want more than the %d it had before the probes", after, before)
	}
	m := readMetaNow(t, cfg)
	if m.WedgedSince != "" {
		t.Errorf("wedged_since = %q for a silent child that answered every nudge, want empty", m.WedgedSince)
	}
	if got := hostLog(t, cfg); strings.Contains(got, "marking it wedged") {
		t.Errorf("the host wedged a child that answered its nudges; log:\n%s", got)
	}
}

// TestLivenessProbeNeverWritesToTheChildsStdin is the guard on HOW the probe
// asks: a resize/SIGWINCH, never bytes. Anything written to the child's stdin
// is read by it as literal keystrokes and would inject text into whatever
// prompt it is showing. Two independent observations, because either alone has
// a gap: the child records every byte its stdin delivers (which a canonical
// line discipline only does for complete lines), and the client's output
// stream is checked for anything but the child's own markers (the terminal
// echoes what is written to the master, so injected bytes would surface there
// even unterminated).
func TestLivenessProbeNeverWritesToTheChildsStdin(t *testing.T) {
	p := compressLivenessTiming(t)
	cfg, stdinLog := probeChildServeConfig(t)
	h := startServeWithConfig(t, cfg)

	conn := h.dial(t)
	out := drainClient(t, conn)
	resizeUntilRepaint(t, conn, out, probeChildWinch)

	time.Sleep(3 * probeCycle(p))
	if got := hostLog(t, cfg); !strings.Contains(got, "nudged pty to force a repaint") {
		t.Fatalf("the host never probed, so this test proves nothing; log:\n%s", got)
	}

	if data, err := os.ReadFile(stdinLog); err == nil && len(data) > 0 {
		t.Errorf("the probe wrote %d bytes to the child's stdin: %q", len(data), data)
	} else if err != nil && !os.IsNotExist(err) {
		t.Fatalf("reading the child's stdin log: %v", err)
	}

	residue := out.String()
	for _, known := range []string{probeChildBanner, probeChildWinch, "\r", "\n"} {
		residue = strings.ReplaceAll(residue, known, "")
	}
	if residue != "" {
		t.Errorf("the client saw %q beyond the child's own output, so something was written to the pty", residue)
	}
}

// TestLivenessMarksAndClearsAWedgedChild is cases (3) and (4) in one run,
// because (4) needs (3)'s state: the W1.T1 fixture with output stopped is
// alive, reads its input and ignores SIGWINCH — indistinguishable from an
// idle session except that it does not repaint when nudged. wedged_since must
// appear after the probe deadline and vanish the moment output resumes, and
// the host must keep serving throughout: afk is not what wedged.
func TestLivenessMarksAndClearsAWedgedChild(t *testing.T) {
	compressLivenessTiming(t)
	cfg, ctrlDir := stuckChildServeConfig(t)
	h := startServeWithConfig(t, cfg)

	conn := h.dial(t)
	out := drainClient(t, conn)
	sendFrame(t, conn, Resize, EncodeResize(40, 120))
	awaitBuffer(t, out, stuckChildHeartbeatMarker)

	setStuckChildFlag(t, ctrlDir, stuckChildNoWriteFlag, true)

	m := awaitMeta(t, cfg, "recorded wedged_since for a child that stopped writing", func(m Meta) bool {
		return m.WedgedSince != ""
	})
	wedgedSince, err := time.Parse(time.RFC3339, m.WedgedSince)
	if err != nil {
		t.Fatalf("wedged_since %q is not the RFC3339 that Meta's other timestamps use: %v", m.WedgedSince, err)
	}
	if age := time.Since(wedgedSince); age < 0 || age > testBudget {
		t.Errorf("wedged_since is %s old, want a plausible detection time", age)
	}
	if got := hostLog(t, cfg); !strings.Contains(got, "marking it wedged") {
		t.Errorf("the wedge is not in host.log, the only record an operator has; log:\n%s", got)
	}
	if got := hostLog(t, cfg); !strings.Contains(got, "nudged pty to force a repaint") {
		t.Errorf("the host wedged the session without probing it first; log:\n%s", got)
	}
	if h.returned() {
		t.Fatalf("Serve returned when its CHILD wedged: %v", h.err)
	}

	setStuckChildFlag(t, ctrlDir, stuckChildNoWriteFlag, false)
	cleared := awaitMeta(t, cfg, "cleared wedged_since once output resumed", func(m Meta) bool {
		return m.WedgedSince == ""
	})
	if cleared.LastOutputAt == "" {
		t.Error("last_output_at is empty after the child resumed writing")
	}
	if got := hostLog(t, cfg); !strings.Contains(got, "clearing wedged_since") {
		t.Errorf("the recovery is not in host.log; log:\n%s", got)
	}

	// Still a live session: a fresh client can still attach and see output.
	second := h.dial(t)
	secondOut := drainClient(t, second)
	sendFrame(t, second, Resize, EncodeResize(40, 120))
	awaitBuffer(t, secondOut, stuckChildHeartbeatMarker)
}

// TestLivenessNeverProbesAnUnattachedSession is the other half of the
// false-positive guard: with nobody attached there is no screen to repaint,
// so nudging is pointless work on every idle session a dashboard lists, and
// the host must not do it — nor conclude anything about a child it never
// asked. last_output_at is still published, because observing output costs
// nothing and needs no client.
func TestLivenessNeverProbesAnUnattachedSession(t *testing.T) {
	p := compressLivenessTiming(t)
	cfg, ctrlDir := stuckChildServeConfig(t)
	h := startServeWithConfig(t, cfg)

	awaitMeta(t, cfg, "published last_output_at with no client attached", func(m Meta) bool {
		return m.LastOutputAt != ""
	})
	setStuckChildFlag(t, ctrlDir, stuckChildNoWriteFlag, true)
	time.Sleep(3 * probeCycle(p))

	if m := readMetaNow(t, cfg); m.WedgedSince != "" {
		t.Errorf("wedged_since = %q for a session nobody was attached to, want empty", m.WedgedSince)
	}
	if got := hostLog(t, cfg); strings.Contains(got, "nudged pty to force a repaint") {
		t.Errorf("the host probed a session with no client attached; log:\n%s", got)
	}
	if h.returned() {
		t.Fatalf("Serve returned while idle: %v", h.err)
	}
}

// TestShippedLivenessScheduleIsTheDocumentedOne pins the production timing
// values themselves, the way TestRestartLadderIsGracefulBeforeItIsFatal pins
// the shipped ladder — and for the reason that test's issues.md entry
// recorded: every behavioural test in this file compresses livenessTiming
// first, so a bad shipped value (say, a silence threshold short enough to
// flag every idle-at-a-prompt session as wedged, the false positive the whole
// detector is designed around avoiding) would leave the entire suite green.
// The relations are asserted alongside the exact values because they are the
// actual safety argument; the values are the agreed rendering of it.
func TestShippedLivenessScheduleIsTheDocumentedOne(t *testing.T) {
	if outputSilenceThreshold != 30*time.Second {
		t.Errorf("outputSilenceThreshold = %v, want 30s: much shorter starts probing sessions that are merely waiting at a prompt", outputSilenceThreshold)
	}
	if wedgeProbeDeadline != 5*time.Second {
		t.Errorf("wedgeProbeDeadline = %v, want 5s: a repaint costs milliseconds and the rest is margin against the expensive mistake, a false wedge", wedgeProbeDeadline)
	}
	if wedgeProbeInterval != 60*time.Second {
		t.Errorf("wedgeProbeInterval = %v, want 60s: an hour-long legitimate silence must not be nudged on every tick", wedgeProbeInterval)
	}
	if livenessTickInterval != time.Second {
		t.Errorf("livenessTickInterval = %v, want 1s: a verdict or recovery must land within one 2s dashboard refresh", livenessTickInterval)
	}
	if livenessPublishInterval != 5*time.Second {
		t.Errorf("livenessPublishInterval = %v, want 5s: a chatty child must not turn every PTY read into a host.json rewrite", livenessPublishInterval)
	}
	if outputSilenceThreshold < 4*wedgeProbeDeadline {
		t.Errorf("outputSilenceThreshold (%v) is not several times wedgeProbeDeadline (%v): silence must be a rare event before it is worth a probe (shipped ratio is 6x)", outputSilenceThreshold, wedgeProbeDeadline)
	}
	if livenessTickInterval >= wedgeProbeDeadline {
		t.Errorf("livenessTickInterval (%v) must be well under wedgeProbeDeadline (%v) or the monitor cannot time a probe out accurately", livenessTickInterval, wedgeProbeDeadline)
	}

	want := livenessPolicy{
		silence:  outputSilenceThreshold,
		deadline: wedgeProbeDeadline,
		interval: wedgeProbeInterval,
		tick:     livenessTickInterval,
		publish:  livenessPublishInterval,
	}
	if livenessTiming != want {
		t.Errorf("livenessTiming = %+v, want the documented constants %+v: the runtime reads the var, not the constants, so drifting it IS a shipped-schedule change", livenessTiming, want)
	}
}
