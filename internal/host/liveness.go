package host

import (
	"fmt"
	"sync"
	"time"
)

// This file is the host's answer to the incident this work exists for: a
// hosted `claude` stopped responding while the host itself kept serving.
// host.log showed resizes, attaches and detaches being handled right up to
// the final line, so nothing about afk's own plumbing distinguishes that
// session from a working one. The only observable that can, is the child, and
// the only thing the child owes the host is output.
//
// Output silence on its own is not evidence. A healthy claude waiting at a
// prompt writes nothing for as long as the user takes to answer, so a
// detector that read silence as a wedge would flag every waiting session in
// the dashboard. Silence therefore only opens the question; the answer comes
// from a probe — the same column-nudge resize the host already performs when
// a client attaches (hostRuntime.nudgeRepaint), which delivers SIGWINCH and
// which any live TUI answers with a repaint. Nothing is ever written to the
// child's stdin: those bytes are read as literal keystrokes and would inject
// text into whatever prompt the child is showing.
//
// The verdict is advisory in the same sense as Alive(): a child blocked in a
// long synchronous computation can miss a probe deadline and be reported
// wedged. That is why the first byte of output clears wedged_since
// immediately, so a false positive costs a stale dashboard cell for one tick
// rather than a wrong permanent state.

const (
	// outputSilenceThreshold is how long the child must produce nothing
	// before the host probes it at all. Deliberately generous: the ordinary
	// reason a session is quiet is that it is waiting for its user, and
	// probing that is pointless work on every idle session. 30s is far
	// longer than the gap between repaints of a working claude, which
	// streams tokens and animates while it is busy, and far shorter than a
	// human's think time at a prompt.
	outputSilenceThreshold = 30 * time.Second
	// wedgeProbeDeadline is how long after the nudge output must appear for
	// the child to count as alive. Answering a SIGWINCH is one turn of an
	// event loop, not work, so a responsive TUI repaints in milliseconds;
	// the rest of this budget is margin for a child that is briefly busy
	// rather than wedged, since a false positive here is the expensive
	// mistake.
	wedgeProbeDeadline = 5 * time.Second
	// wedgeProbeInterval is the minimum gap between two probes of one
	// session. Without it, a session that is legitimately silent for an
	// hour would be nudged on every tick for an hour: harmless per nudge,
	// but it makes the host's log unreadable and reflows the child's
	// screen for no reason. Measured from the last probe, so a session that
	// answers and falls quiet again is not immediately re-shaken.
	wedgeProbeInterval = 60 * time.Second
	// livenessTickInterval is how often the monitor re-evaluates. It bounds
	// how late a verdict or a recovery can be, so it is much shorter than
	// the deadline it is measuring against; one second also keeps a
	// recovery visible within a single 2s dashboard refresh.
	livenessTickInterval = time.Second
	// livenessPublishInterval rate-limits host.json rewrites for a session
	// whose only change is a newer last_output_at. A chatty child produces
	// output continuously and republishing per read would rewrite the file
	// thousands of times a second for a field the dashboard reads every 2s.
	// Wedge and recovery transitions ignore this and publish at once.
	livenessPublishInterval = 5 * time.Second
)

// livenessPolicy is the detector's schedule. Copied into each hostRuntime at
// construction rather than read from the package var below on every tick, so
// a test that compresses the schedule cannot race the monitor goroutine.
type livenessPolicy struct {
	silence  time.Duration
	deadline time.Duration
	interval time.Duration
	tick     time.Duration
	publish  time.Duration
}

// livenessTiming is the schedule every host is built with. Package-level, for
// the same reason resizeNudgeDelay is, so tests can compress it; the shipped
// values are the documented constants above.
var livenessTiming = livenessPolicy{
	silence:  outputSilenceThreshold,
	deadline: wedgeProbeDeadline,
	interval: wedgeProbeInterval,
	tick:     livenessTickInterval,
	publish:  livenessPublishInterval,
}

// probeDecision is what one tick of the liveness monitor should do.
type probeDecision int

const (
	// probeIdle: nothing to do — the child is talking, nobody is attached,
	// or a probe is still inside its deadline.
	probeIdle probeDecision = iota
	// probeNudge: silent long enough with a client attached; ask the child
	// to repaint.
	probeNudge
	// probeWedge: the nudge went unanswered past the deadline.
	probeWedge
)

func (d probeDecision) String() string {
	switch d {
	case probeIdle:
		return "idle"
	case probeNudge:
		return "nudge"
	case probeWedge:
		return "wedge"
	default:
		return fmt.Sprintf("probeDecision(%d)", int(d))
	}
}

// probeInput is everything decideProbe branches on.
type probeInput struct {
	now time.Time
	// attached is whether a client currently holds the session.
	attached bool
	// silentSince is when the child was last known to be producing output,
	// or the host's start time if it never has.
	silentSince time.Time
	// probedAt is when the last nudge was sent, zero if none ever was.
	probedAt time.Time
	// probing is whether that nudge is still awaiting an answer.
	probing bool
}

// decideProbe is the whole detection policy, pure so the matrix is
// table-testable without a PTY, a child or a clock — following this repo's
// convention for anything that branches on live state (shouldHandOffToDash,
// routeArgs, classifyInput).
//
// The ordering matters. The attachment check comes first because an
// unattached session is never probed, however long it has been quiet: there
// is no screen to repaint, so the nudge asks nothing useful, and a verdict
// reached with nobody watching would be recorded off the back of a question
// nobody heard the answer to. The pending-probe check comes before the
// silence check because once a probe is out, the only thing that matters is
// whether it was answered in time.
func decideProbe(in probeInput, p livenessPolicy) probeDecision {
	if !in.attached {
		return probeIdle
	}
	if in.probing {
		if in.now.Sub(in.probedAt) < p.deadline {
			return probeIdle
		}
		return probeWedge
	}
	if in.now.Sub(in.silentSince) < p.silence {
		return probeIdle
	}
	if !in.probedAt.IsZero() && in.now.Sub(in.probedAt) < p.interval {
		return probeIdle
	}
	return probeNudge
}

// liveness is one session's output-liveness state. Written from the PTY pump
// (noteOutput, on every read) and from the liveness monitor (everything
// else), hence its own mutex: the pump must never contend with the host's
// client bookkeeping on hostRuntime.mu, which is on the same hot path.
type liveness struct {
	policy livenessPolicy

	mu sync.Mutex
	// started is when the host began, used as the silence baseline until the
	// child has written anything. Measuring from the zero time instead would
	// report every freshly spawned session as silent since the epoch.
	started time.Time
	// lastOutput is when the child last wrote, zero until it has. Kept
	// distinct from started so last_output_at is only published once it
	// describes something that actually happened.
	lastOutput  time.Time
	probedAt    time.Time
	probing     bool
	wedgedSince time.Time
	recovered   time.Duration
	recovery    bool
	dirty       bool
	force       bool
	lastPublish time.Time
}

func newLiveness(p livenessPolicy, started time.Time) *liveness {
	return &liveness{policy: p, started: started}
}

// noteOutput records that the child wrote something. Called from pumpPTY on
// every non-empty read, so it does no I/O and takes no other lock: the
// verdict is cleared here, in memory, the moment output arrives, and the
// monitor republishes host.json and logs the recovery on its next tick.
func (l *liveness) noteOutput(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lastOutput = now
	// Any output answers an outstanding probe, whether or not it was the
	// repaint the nudge asked for.
	l.probing = false
	l.dirty = true
	if l.wedgedSince.IsZero() {
		return
	}
	l.recovered = now.Sub(l.wedgedSince)
	l.recovery = true
	l.wedgedSince = time.Time{}
	l.force = true
}

// observe is one tick's decision. It clears a probe that was outstanding when
// the last client went away: that deadline expired with nobody watching, and
// reattaching nudges the child again anyway, so the next verdict is one a
// client was actually present for.
func (l *liveness) observe(now time.Time, attached bool) probeDecision {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !attached {
		l.probing = false
	}
	return decideProbe(probeInput{
		now:         now,
		attached:    attached,
		silentSince: l.silentSinceLocked(),
		probedAt:    l.probedAt,
		probing:     l.probing,
	}, l.policy)
}

func (l *liveness) silentSinceLocked() time.Time {
	if l.lastOutput.After(l.started) {
		return l.lastOutput
	}
	return l.started
}

// noteProbeSent starts the deadline. Called only once the nudge has actually
// been handed to the serve loop — a probe that was never sent must not be
// allowed to time out.
func (l *liveness) noteProbeSent(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.probedAt = now
	l.probing = true
}

// markWedged records the verdict and reports how long the child had been
// silent, plus whether this is the first verdict of this wedge. Repeat
// verdicts deliberately leave wedged_since at its original value: its whole
// use downstream is the age of the wedge, which a re-probe every interval
// would otherwise keep resetting to zero.
func (l *liveness) markWedged(now time.Time) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// The probe is concluded either way; probedAt stays, so the interval
	// still rate-limits the next one.
	l.probing = false
	if !l.wedgedSince.IsZero() {
		return 0, false
	}
	l.wedgedSince = now
	l.dirty = true
	l.force = true
	return now.Sub(l.silentSinceLocked()), true
}

// takeRecovery reports a wedge that has ended, exactly once, so the monitor
// can log it. The clearing itself already happened in noteOutput.
func (l *liveness) takeRecovery() (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.recovery {
		return 0, false
	}
	l.recovery = false
	return l.recovered, true
}

// livenessSnapshot is the pair of host.json fields this package owns, already
// formatted the way Meta's other timestamps are.
type livenessSnapshot struct {
	LastOutputAt string
	WedgedSince  string
}

// publishDue reports whether host.json should be rewritten now, and with
// what. A wedge or a recovery publishes immediately; a merely newer
// last_output_at waits for the publish interval, so a chatty child does not
// turn every PTY read into a file rewrite.
func (l *liveness) publishDue(now time.Time) (livenessSnapshot, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.dirty {
		return livenessSnapshot{}, false
	}
	if !l.force && now.Sub(l.lastPublish) < l.policy.publish {
		return livenessSnapshot{}, false
	}
	l.dirty = false
	l.force = false
	l.lastPublish = now
	return livenessSnapshot{
		LastOutputAt: rfc3339OrEmpty(l.lastOutput),
		WedgedSince:  rfc3339OrEmpty(l.wedgedSince),
	}, true
}

// rfc3339OrEmpty formats t the way Meta.StartedAt is written, mapping the
// zero time to "" — the documented "unknown" for last_output_at and "not
// wedged" for wedged_since (both fields are omitempty, so an empty string
// means the key is absent, exactly as an older host would leave it).
func rfc3339OrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// monitorLiveness evaluates the child's liveness on its own goroutine. It is
// deliberately not part of the serve loop: waiting out a probe deadline is
// the one thing a loop that must also answer detach, exit and signals cannot
// do. All it asks of the serve loop is the nudge itself, through a
// single-slot channel it never blocks on.
func (h *hostRuntime) monitorLiveness() {
	ticker := time.NewTicker(h.live.policy.tick)
	defer ticker.Stop()

	for {
		select {
		case <-h.done:
			return
		case now := <-ticker.C:
			h.evaluateLiveness(now)
		}
	}
}

func (h *hostRuntime) evaluateLiveness(now time.Time) {
	if d, ok := h.live.takeRecovery(); ok {
		h.log.Printf("session %q produced output again after %s wedged; clearing wedged_since",
			h.cfg.Name, d.Round(time.Millisecond))
	}

	switch d := h.live.observe(now, h.attached()); d {
	case probeNudge:
		// Only start the deadline if the request was actually accepted. A
		// full slot means the serve loop has not picked up the previous
		// one, which is itself evidence the host is busy rather than the
		// child idle; the next tick tries again.
		if h.requestProbe() {
			h.live.noteProbeSent(now)
		}
	case probeWedge:
		if silentFor, first := h.live.markWedged(now); first {
			h.log.Printf("session %q has produced no output for %s and did not repaint within %s of a nudge; marking it wedged",
				h.cfg.Name, silentFor.Round(time.Millisecond), h.live.policy.deadline)
		}
	case probeIdle:
	default:
		h.log.Printf("unhandled probe decision %v", d)
	}

	if snap, ok := h.live.publishDue(now); ok {
		h.publishLiveness(snap)
	}
}

// requestProbe asks the serve loop to nudge the child. Never blocks: the
// monitor must not be able to park behind the serve loop, and a dropped
// request costs one tick.
func (h *hostRuntime) requestProbe() bool {
	select {
	case h.probes <- struct{}{}:
		return true
	default:
		return false
	}
}

// publishLiveness rewrites host.json with the current liveness fields on top
// of the host's base metadata. The host process is still host.json's only
// writer, but baseMeta itself has two writers: Serve, before this goroutine
// starts (the `go` statement is that ordering's happens-before edge), and
// restart.go's setResumedChild, from the serve loop while this monitor is
// live. That second writer is why baseMeta is read under publishMu here —
// setResumedChild takes the same mutex, so a publish can never observe a
// half-written argv/child_pid pair.
//
// publishMu also closes the shutdown window. Serve's deferred cleanup removes
// host.json, and a publish landing after that would recreate a file naming a
// pid that no longer exists — a dead session that looks reachable. shutdown
// calls stopPublishing under the same mutex, so once it returns no publish is
// in flight and none can start.
//
// Known, accepted cost: holding publishMu across writeMetaFile means the
// serve loop can wait here for the length of one atomic write when a restart's
// setResumedChild contends with a publish already in flight. That wait is one
// same-directory temp-file write plus a rename — bounded, local-disk work,
// nothing like the unbounded socket/PTY waits this package exists to keep off
// the loop. Narrowing the mutex to a snapshot-then-write would reopen the
// shutdown window above, which is the worse trade.
func (h *hostRuntime) publishLiveness(snap livenessSnapshot) {
	h.publishMu.Lock()
	defer h.publishMu.Unlock()
	if h.publishOff {
		return
	}
	m := h.baseMeta
	m.LastOutputAt = snap.LastOutputAt
	m.WedgedSince = snap.WedgedSince
	if err := writeMetaFile(h.cfg.MetaPath, m); err != nil {
		h.log.Printf("republishing %s with the session's liveness: %v", h.cfg.MetaPath, err)
	}
}

// stopPublishing closes the window described on publishLiveness. Idempotent.
func (h *hostRuntime) stopPublishing() {
	h.publishMu.Lock()
	defer h.publishMu.Unlock()
	h.publishOff = true
}
