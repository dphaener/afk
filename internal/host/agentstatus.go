package host

import (
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/dphaener/afk/internal/progstatus"
	"github.com/dphaener/afk/internal/provider"
)

// This file keeps what the hosted agent last said about itself and how the
// previous child ended, for host.json's agent_status and last_exit.
//
// Agent CLIs announce their state with Program status (OSC 7501) reports in
// their own terminal output. The host already reads every byte of that output
// in pumpPTY, so it is the one place those reports can be seen without asking
// the agent anything. The pump feeds each chunk through a progstatus.Scanner
// and hands what it finds to an agentKeeper, which only updates memory. The
// file write happens on monitorAgentStatus's own goroutine, so a slow disk or
// a reader holding host.json can never make the pump stop draining the PTY
// (a full PTY buffer would block the child).

const (
	// agentStatusPublishInterval is the fewest seconds between two host.json
	// rewrites for a status whose state did not change (a new msg, say). An
	// agent can rewrite its message many times a second while it works, and
	// every rewrite of host.json is also a change event on the web page's
	// live stream (internal/web fingerprints the file), so the rate is capped.
	// A change of state skips the wait: that is the change people look for.
	agentStatusPublishInterval = 2 * time.Second
	// agentStatusTickInterval is how often the publisher checks for a held
	// change without being nudged. The pump nudges only for a state change,
	// so a msg-only change that arrives after a quiet spell is found here.
	agentStatusTickInterval = 250 * time.Millisecond
)

// agentStatusPolicy is the publisher's schedule, copied into each host at
// construction like livenessPolicy, so a test that compresses it cannot race
// a running host.
type agentStatusPolicy struct {
	publish time.Duration
	tick    time.Duration
}

// agentStatusTiming is the schedule every host is built with. Package-level
// so tests can compress it; the shipped values are the constants above.
var agentStatusTiming = agentStatusPolicy{
	publish: agentStatusPublishInterval,
	tick:    agentStatusTickInterval,
}

// hostClock is the host's clock for agent_status and last_exit times and for
// their publish schedule. Package-level, copied into each host by
// newHostRuntime, so a test can drive it.
var hostClock = time.Now

// agentSnapshot is the pair of host.json fields this file owns. The pointers
// are fresh copies, never written after publishDue returns them, so the
// publisher can keep one without a lock of the keeper's.
type agentSnapshot struct {
	Status *AgentStatus
	Exit   *LastExit
}

// agentKeeper holds the latest root record from the session's own agent.
// Written from the PTY pump (observe) and the serve loop (reset, recordExit),
// read by monitorAgentStatus, hence its own mutex: like liveness, it must
// never make the pump wait on anything that does I/O.
type agentKeeper struct {
	policy agentStatusPolicy
	// own is the session's provider, nil when ServeConfig.Provider names
	// none this binary knows. Then only reports with no app word are kept,
	// since none can be shown to be the agent's.
	own provider.Provider

	mu sync.Mutex
	// epoch counts restarts. Each child generation's pump carries the epoch
	// it was spawned in, and a report from an older one is ignored: after a
	// restart the retired pump can still be handing over the dead child's
	// last bytes, and those must not become the new agent's status.
	epoch uint64
	cur   *AgentStatus
	exit  *LastExit
	// publishedState is the state host.json last carried ("" for none),
	// which is what decides whether a change skips the publish interval.
	publishedState string
	dirty          bool
	force          bool
	lastPublish    time.Time
}

// newAgentKeeper builds a keeper for a session of the named provider (empty
// meaning the default). The provider is resolved through internal/provider
// rather than compared by name here, so this package never names one.
func newAgentKeeper(p agentStatusPolicy, providerName string) *agentKeeper {
	k := &agentKeeper{policy: p}
	if own, err := provider.Lookup(providerName); err == nil {
		k.own = own
	}
	return k
}

// currentEpoch is the epoch a newly spawned generation reports under.
func (k *agentKeeper) currentEpoch() uint64 {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.epoch
}

// ours reports whether r is a root record the session's own agent sent. A
// record with an id is one of the agent's sub-tasks, not the agent; a record
// whose app is another tool (a build run inside the agent's terminal, say) is
// that tool's. A record with no app is kept because the agent is the
// terminal's program and the spec lets a program leave app out.
func (k *agentKeeper) ours(r progstatus.Record) bool {
	if r.ID != "" {
		return false
	}
	if r.App == "" {
		return true
	}
	p, ok := provider.FromApp(r.App)
	return ok && k.own != nil && p == k.own
}

// observe takes the reports one PTY read held, in order, from the generation
// spawned in epoch, and reports whether a publish is now forced (the state
// differs from what host.json carries). No I/O, called on every read that
// held a report.
//
// A report that repeats the current state, kind and msg changes nothing, not
// even At: agents resend their status, and taking each resend as news would
// rewrite host.json every 2 seconds for nothing. So At is when the agent first
// said this, as seen by the host.
func (k *agentKeeper) observe(epoch uint64, reports []progstatus.Report, now time.Time) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	if epoch != k.epoch {
		return false
	}
	for _, r := range reports {
		if !k.ours(r.Record) {
			continue
		}
		if r.State == progstatus.StateClear {
			if k.cur != nil {
				k.cur = nil
				k.dirty = true
			}
			continue
		}
		next := AgentStatus{State: string(r.State), Kind: string(r.Kind), Msg: r.Msg}
		if k.cur != nil && k.cur.State == next.State && k.cur.Kind == next.Kind && k.cur.Msg == next.Msg {
			continue
		}
		next.At = rfc3339OrEmpty(now)
		k.cur = &next
		k.dirty = true
	}
	if k.dirty && k.stateLocked() != k.publishedState {
		k.force = true
	}
	return k.dirty && k.force
}

func (k *agentKeeper) stateLocked() string {
	if k.cur == nil {
		return ""
	}
	return k.cur.State
}

// reset forgets the record and starts a new epoch. Called on a restart in
// place, before the new child is spawned: the new agent has said nothing yet,
// and the old agent's status would describe a process that no longer exists.
func (k *agentKeeper) reset() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.epoch++
	if k.cur != nil {
		k.cur = nil
		k.dirty = true
		k.force = true
	}
}

// recordExit keeps how a retired generation ended, for last_exit. Published
// at once: a restart is rare and the exit is what someone looking at it wants.
func (k *agentKeeper) recordExit(e LastExit) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.exit = &e
	k.dirty = true
	k.force = true
}

// publishDue returns what host.json should now carry, when there is a change
// and either it is forced or the publish interval has passed since the last
// publish. Otherwise nothing.
func (k *agentKeeper) publishDue(now time.Time) (agentSnapshot, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.dirty {
		return agentSnapshot{}, false
	}
	if !k.force && now.Sub(k.lastPublish) < k.policy.publish {
		return agentSnapshot{}, false
	}
	k.dirty = false
	k.force = false
	k.lastPublish = now
	k.publishedState = k.stateLocked()
	var snap agentSnapshot
	if k.cur != nil {
		c := *k.cur
		snap.Status = &c
	}
	if k.exit != nil {
		e := *k.exit
		snap.Exit = &e
	}
	return snap, true
}

// nextWake is how long the publisher may sleep: until a held change falls
// due, or one tick when nothing is held.
func (k *agentKeeper) nextWake(now time.Time) time.Duration {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.dirty && !k.force {
		if d := k.policy.publish - now.Sub(k.lastPublish); d > 0 && d < k.policy.tick {
			return d
		}
	}
	return k.policy.tick
}

// lastExitOf turns a reaped child's state into a last_exit record. A nil
// state (Wait failed before the child could be reaped) is code -1 with no
// signal, the same "did not exit normally" ExitCode reports.
func lastExitOf(ps *os.ProcessState, at time.Time) LastExit {
	e := LastExit{Code: -1, At: rfc3339OrEmpty(at)}
	if ps == nil {
		return e
	}
	e.Code = ps.ExitCode()
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		e.Signal = signalName(ws.Signal())
	}
	return e
}

// signalNames spells the signals a child is commonly killed by the way people
// write them. syscall.Signal's String gives the description ("terminated"),
// and golang.org/x/sys, which has a full table, is not a direct dependency.
var signalNames = map[syscall.Signal]string{
	syscall.SIGHUP:  "SIGHUP",
	syscall.SIGINT:  "SIGINT",
	syscall.SIGQUIT: "SIGQUIT",
	syscall.SIGILL:  "SIGILL",
	syscall.SIGTRAP: "SIGTRAP",
	syscall.SIGABRT: "SIGABRT",
	syscall.SIGBUS:  "SIGBUS",
	syscall.SIGFPE:  "SIGFPE",
	syscall.SIGKILL: "SIGKILL",
	syscall.SIGUSR1: "SIGUSR1",
	syscall.SIGSEGV: "SIGSEGV",
	syscall.SIGUSR2: "SIGUSR2",
	syscall.SIGPIPE: "SIGPIPE",
	syscall.SIGALRM: "SIGALRM",
	syscall.SIGTERM: "SIGTERM",
	syscall.SIGXCPU: "SIGXCPU",
	syscall.SIGXFSZ: "SIGXFSZ",
}

func signalName(s syscall.Signal) string {
	if n, ok := signalNames[s]; ok {
		return n
	}
	return fmt.Sprintf("signal %d", int(s))
}

// nudgeAgentStatus wakes the publisher. Never blocks: it is called from the
// PTY pump, and a full slot already means the publisher will look.
func (h *hostRuntime) nudgeAgentStatus() {
	select {
	case h.agentNudge <- struct{}{}:
	default:
	}
}

// monitorAgentStatus republishes host.json when the keeper says a change is
// due. It wakes on a nudge (a state change, a restart), on its tick (a held or
// msg-only change) and at the moment a held change falls due.
func (h *hostRuntime) monitorAgentStatus() {
	timer := time.NewTimer(h.agent.policy.tick)
	defer timer.Stop()
	for {
		select {
		case <-h.done:
			return
		case <-h.agentNudge:
		case <-timer.C:
		}
		now := h.now()
		if snap, ok := h.agent.publishDue(now); ok {
			h.publishAgentStatus(snap)
		}
		// Go 1.23 timers: Reset needs no drain, a stale tick is discarded.
		timer.Reset(h.agent.nextWake(now))
	}
}

// publishAgentStatus records snap as host.json's agent fields and rewrites
// the file through the one publish path, so the write also carries the base
// metadata and the liveness fields last published.
func (h *hostRuntime) publishAgentStatus(snap agentSnapshot) {
	h.publish("the agent's status", func() { h.pubAgent = snap })
}
