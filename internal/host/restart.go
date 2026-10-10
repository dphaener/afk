package host

import (
	"fmt"
	"path/filepath"
	"syscall"
	"time"

	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/state"
)

// restart.go is in-place recovery for the incident this work exists for: a
// hosted child (`claude`, or `codex` for a codex session) that stops
// responding while the host around it keeps serving. The host cannot un-wedge
// the child — the defect is in the child — but it can replace it without
// losing the conversation, because the session id is recorded in status.json
// and the provider's resume command (`claude --resume <id>`,
// `codex resume <id>`) picks that same conversation back up.
//
// A restart is split across two goroutines, and the split is load-bearing.
// Everything that touches hostRuntime's cur, rows or cols — validating the
// request (beginRestart) and swapping the generations (finishRestart) — runs
// on the serve loop, which is what keeps that bookkeeping safe without a
// lock. The one unbounded part, waiting out the ladder's graces (~9s shipped
// against a child that ignores SIGINT and SIGTERM), runs on its own goroutine
// that only signals the OLD child and watches its exited channel: it mutates
// nothing the loop owns. An earlier revision ran the whole restart inline on
// the loop, bounded by the graces; review rejected that as a miniature of the
// incident itself — with the loop parked, readClient's blocking ctrl sends
// park too, the socket stops being drained, and the attached pane's own 5s
// write deadline closes its connection. h.restarting is the loop's own flag
// for the in-flight window: it refuses a second Restart and disables the
// child-exited shutdown branch, which would otherwise read the ladder's kill
// as "child exited on its own".

const (
	// childIntGrace is how long the child gets to exit after SIGINT before
	// the ladder escalates. SIGINT leads because it is what ^C does: a child
	// still processing signals gets to run its own teardown — restoring the
	// terminal, flushing whatever it was writing — instead of dying mid-write.
	// A genuinely wedged child will not answer it at all, so the grace is the
	// shortest of the three: this is the rung most likely to be spent waiting
	// for nothing.
	childIntGrace = 2 * time.Second
)

// rung is one step of the restart signal ladder: a signal, and how long the
// child gets to act on it before the next one.
type rung struct {
	sig   syscall.Signal
	grace time.Duration
}

// restartLadder escalates SIGINT → SIGTERM → SIGKILL, reusing the graces
// terminateChild already uses for its last two rungs so there is one set of
// numbers for "how long does a child get to die". Package-level for the same
// reason resizeNudgeDelay and livenessTiming are: no test can afford the
// shipped worst case.
var restartLadder = []rung{
	{syscall.SIGINT, childIntGrace},
	{syscall.SIGTERM, childTermGrace},
	{syscall.SIGKILL, childKillGrace},
}

// escalate walks ladder, sending each signal and then waiting out its grace,
// and stops the moment exited closes. It reports the signals it actually sent
// and whether the child is gone.
//
// Pure in the sense this package means it (classifyInput, decideProbe): the
// process is reached only through send and observed only through exited, so
// the entire matrix — a child that dies on the first signal, one that ignores
// everything short of SIGKILL, one that cannot be killed at all — is testable
// without a process. exited is checked before every signal too, so a child
// reaped between the restart request and this call is never signalled.
func escalate(ladder []rung, exited <-chan struct{}, send func(syscall.Signal)) ([]syscall.Signal, bool) {
	var sent []syscall.Signal
	for _, r := range ladder {
		select {
		case <-exited:
			return sent, true
		default:
		}
		send(r.sig)
		sent = append(sent, r.sig)
		select {
		case <-exited:
			return sent, true
		case <-time.After(r.grace):
		}
	}
	select {
	case <-exited:
		return sent, true
	default:
	}
	return sent, false
}

// resumeArgv is the respawned child's command line, and the whole of it. The
// program comes from the provider the host was started with (ServeConfig's
// Provider, empty meaning claude), so a codex session resumes under codex
// rather than being handed to claude with an id claude has never seen. An
// unknown provider is an error rather than a fallback to claude for the same
// reason: the wrong binary cannot resume the conversation, and the child it
// would replace is the only place the session's unsaved state lives.
//
// No prompt argument — internal/session passes `/afk:go --session <name>`
// when it FIRST spawns a session, and that prompt runs the afk lifecycle.
// The lifecycle has already run by the time anything is wedged, so passing it
// again would restart completed work.
//
// And no --fork-session: it mints a new session id, which would orphan the id
// recorded in status.json and silently break hook targeting
// (status-schema.md §7 matches hooks on session_id equality), killing that
// session's notifications and awaiting_input writes. Plain --resume reuses the
// recorded id, which is also why a restart never writes to status.json at all.
func resumeArgv(providerName, sessionID string) ([]string, error) {
	p, err := provider.Lookup(providerName)
	if err != nil {
		return nil, err
	}
	return p.ResumeArgv(sessionID), nil
}

// statusPath is the session's status.json: host.json's documented sibling,
// both inside <worktree>/.afk/<name>/ (internal/session's statusDir).
// Derived from the MetaPath the host was handed rather than rebuilt from
// Worktree and Name, for the same reason writeMetaFile honors that flag — the
// path is configuration, and deriving a second copy of it invites the two to
// disagree.
func (h *hostRuntime) statusPath() string {
	return filepath.Join(filepath.Dir(h.cfg.MetaPath), "status.json")
}

// resumeSessionID reads the conversation to resume from status.json ON DISK,
// every time. Never from memory and never from a value cached at host start:
// status.json is the one multi-writer file in this design — the conductor and
// Claude Code's hooks rewrite it throughout a session's life — so the only id
// worth resuming is the one recorded now.
//
// The read goes through internal/state, which implements status-schema.md's
// reader rules verbatim, including rejecting unknown keys as corruption.
// Anything it calls stale is a refusal here rather than a guess: a wrong id
// joins somebody else's conversation, and no id at all would start a fresh
// one on top of finished work.
func resumeSessionID(path string) (string, error) {
	res := state.ReadStatus(path)
	if res.Stale {
		return "", fmt.Errorf("%s is not usable (%s): %s", path, res.Reason, res.Detail)
	}
	if res.Status.SessionID == "" {
		return "", fmt.Errorf("%s records no session_id, so there is no conversation to resume", path)
	}
	return res.Status.SessionID, nil
}

// restartHandoff is what the ladder goroutine hands back to the serve loop:
// the generation it walked the ladder against, the argv finishRestart should
// respawn, and how the ladder ended.
type restartHandoff struct {
	old  *childProc
	argv []string
	sent []syscall.Signal
	gone bool
}

// beginRestart handles the Restart frame on the serve loop: validate the
// request, refuse it outright when there is no conversation to resume, and
// otherwise start the signal ladder on its own goroutine. It never blocks
// beyond one status.json read — the loop must stay free to answer resize and
// detach for the whole of the ladder's graces (see this file's header).
//
// The session id and the provider are both resolved BEFORE the child is
// touched, on purpose. A session whose conversation cannot be found, or whose
// provider this binary does not know, is still a session, and its child is
// the only place its unsaved state lives.
func (h *hostRuntime) beginRestart() {
	if h.restarting {
		h.log.Printf("ignoring a Restart frame for session %q: a restart is already in flight", h.cfg.Name)
		h.notifyClient("restart already in progress")
		return
	}
	argv, err := h.restartArgv()
	if err != nil {
		h.log.Printf("refusing to restart session %q: %v", h.cfg.Name, err)
		h.notifyClient("restart failed: %v", err)
		h.sendRestartResult(err)
		return
	}

	old := h.cur
	h.log.Printf("restarting session %q: stopping child (pid %d), resuming as %q", h.cfg.Name, old.pid(), argv)
	h.restarting = true
	go func() {
		sent, gone := escalate(restartLadder, old.exited, func(sig syscall.Signal) {
			h.log.Printf("restart: sending %v to child (pid %d)", sig, old.pid())
			h.signal(old, sig)
		})
		// Buffered 1 and guarded by h.restarting, so this send can never
		// block — not even against a loop that has already returned.
		h.restartDone <- restartHandoff{old: old, argv: argv, sent: sent, gone: gone}
	}()
}

// restartArgv resolves everything a restart needs before the child is
// touched: the recorded session id and the configured provider's resume
// command for it. Either failing is a refusal, reported by beginRestart.
func (h *hostRuntime) restartArgv() ([]string, error) {
	sessionID, err := resumeSessionID(h.statusPath())
	if err != nil {
		return nil, err
	}
	return resumeArgv(h.cfg.Provider, sessionID)
}

// finishRestart is the serve-loop half of a restart: retire the reaped
// generation, respawn the provider's resume command on a fresh PTY in the same
// worktree, and keep the host process, its socket and the attached client.
//
// The return value follows Serve's convention rather than reporting whether
// the restart worked: nil means the host should keep serving, and an error
// means it can no longer host this session. A ladder the child survived is
// therefore nil — the session is left exactly as it was and the client is
// told why — and the one error is a respawn that failed, which leaves nothing
// to host.
func (h *hostRuntime) finishRestart(hs restartHandoff) error {
	h.restarting = false
	old := hs.old
	if !hs.gone {
		// Starting a second child while the first is still alive is worse
		// than the wedge: two children would share one worktree and one
		// status.json, and only one of them could ever be reaped.
		h.log.Printf("restart: child (pid %d) survived %v; leaving the session as it was", old.pid(), hs.sent)
		failure := fmt.Errorf("the child (pid %d) is still alive after %v", old.pid(), hs.sent)
		h.notifyClient("restart failed: %v", failure)
		h.sendRestartResult(failure)
		return nil
	}
	h.log.Printf("restart: child (pid %d) is gone after %v", old.pid(), hs.sent)
	argv := hs.argv

	// Retired only now that the child is reaped: until this point it is still
	// the live session, and stopping its input writer any earlier would have
	// left a child that DID answer SIGINT unreachable from the keyboard while
	// it shut itself down.
	close(old.retired)
	// The client sees the dying child's last bytes before the new one paints
	// over them, and closing the master afterwards guarantees the retired
	// pump is out of the way rather than racing the resumed child's output.
	//
	// This Close is safe even if writeChild(old) is still parked in a blocked
	// ptmx.Write — the one thing a wedged child can leave in flight forever.
	// The invariant is Go's, not a kernel-ordering hope: this master fd is in
	// BLOCKING mode (SetWriteDeadline fails with "file type does not support
	// deadline", i.e. it was never registered with the runtime poller), and
	// for a blocking fd os.File.Close only MARKS the descriptor closed — the
	// actual close(2) runs when the last in-flight operation releases its
	// reference, and the csema wait is skipped so Close itself never blocks
	// behind the write (go1.24 internal/poll/fd_unix.go, FD.Close lines
	// 91-118: increfAndClose, decref-to-destroy, `if fd.isBlocking == 0`
	// guard on the semaphore). So the blocked write can neither be closed out
	// from under (no fd recycling underneath it) nor delay this restart; it
	// finishes against the doomed descriptor whenever the kernel lets it go —
	// typically at once, since escalate has already reaped the child — and
	// writeChild exits via old.retired.
	h.drainPTY(old)
	if err := old.ptmx.Close(); err != nil {
		h.log.Printf("restart: closing the retired pty master: %v", err)
	}

	if err := h.spawn(argv); err != nil {
		// The old child is reaped and no new one started, so there is nothing
		// left to serve. Staying up would advertise a reachable session that
		// can never produce another byte; shutting down at least reports the
		// truth to the dashboard.
		h.log.Printf("restart: %v; this session has no child left, shutting the host down", err)
		h.notifyClient("restart failed: %v", err)
		h.sendRestartResult(err)
		return err
	}

	// Both before the nudge: the monitor's next tick then republishes
	// host.json with the resumed argv and no wedge whatever the child does
	// with the SIGWINCH, including nothing at all.
	h.live.noteRestart(time.Now())
	h.setResumedChild(argv, h.cur.pid())
	// The attached pane is still showing the dead child's screen, and the
	// resumed child has no idea anyone is looking; the same nudge an attaching
	// client gets is what makes it paint.
	h.nudgeRepaint()
	h.log.Printf("session %q restarted: child pid %d, argv %q", h.cfg.Name, h.cur.pid(), argv)
	h.sendRestartResult(nil)
	return nil
}

// setResumedChild records the resumed child — its pid and its command line —
// as host.json's new base content, leaving the liveness fields to the monitor
// that publishes them.
//
// Meta.PID is deliberately NOT touched. That field is the HOST's pid and the
// host is precisely what survives a restart: internal/discover signal-0s it to
// decide whether a session is reachable, and `afk attach`/`afk stop` use it
// to find the host, so writing the child's pid there would make a live session
// look dead the instant a restart succeeded. The child's pid goes to
// hostMeta.ChildPID (host.go), which exists so the replacement is observable
// from outside this process at all — same pid, same socket, different child.
//
// Meta.Provider is left alone too: a restart resumes under the same provider,
// so host.json keeps naming the binary the resumed child runs.
//
// It takes publishMu because the monitor is live by now and reads baseMeta
// under that same mutex (publishLiveness), which is what makes this second
// writer safe; this is the only place baseMeta is written after Serve's
// startup.
func (h *hostRuntime) setResumedChild(argv []string, childPID int) {
	h.publishMu.Lock()
	defer h.publishMu.Unlock()
	h.baseMeta.Argv = argv
	h.baseMeta.ChildPID = childPID
}

// noteRestart resets the wedge detector for a freshly spawned child: its
// silence starts now, and any verdict the child it replaced had earned is
// gone.
//
// Deliberately not noteOutput, even though that also clears the verdict. The
// resumed child has not written anything yet, so recording output would put a
// fabricated last_output_at in host.json and make the monitor log a recovery
// that never happened. force is what gets the cleared verdict published on the
// next tick instead of waiting out the publish interval.
//
// probedAt survives, exactly as it does in markWedged: the interval still
// rate-limits the next probe, so a restart cannot be used to shake a child
// once per tick.
func (l *liveness) noteRestart(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.started = now
	l.lastOutput = time.Time{}
	l.probing = false
	l.wedgedSince = time.Time{}
	l.dirty = true
	l.force = true
}

// notifyClient puts a one-line host message in the attached client's output
// stream. There is no error frame on this wire — the protocol is Data, Resize,
// Detach, Exit, Stolen and Restart (protocol.go) — and inventing one is not
// this change's to make, so the message travels as Data: the client renders it
// as terminal output, which lands it on the frozen screen the operator is
// already staring at. CRLF because that terminal is in raw mode.
//
// It uses the same non-blocking send as the PTY pump, so a client that has
// stopped reading cannot delay a restart. The log line beside every call is
// the durable record.
func (h *hostRuntime) notifyClient(format string, args ...any) {
	h.mu.Lock()
	c := h.client
	h.mu.Unlock()
	if c == nil {
		return
	}
	msg := fmt.Sprintf("\r\n[afk] "+format+"\r\n", args...)
	if !c.send(Data, []byte(msg)) {
		h.log.Printf("could not tell the attached client %q: its queue is full", msg)
	}
}

// sendRestartResult is notifyClient's typed sibling: the machine-readable
// outcome of a restart, as a RestartResult frame — empty payload for a
// respawned child, the failure's text otherwise. It exists for the requester
// that is not a pane rendering Data frames (the dashboard's dial-and-send
// trigger, internal/ui's defaultSendRestart), which needs an answer it can
// branch on rather than terminal output to grep. Same non-blocking send, same
// reasoning: a client that has stopped reading cannot delay the host, and the
// log lines around every restart are the durable record.
func (h *hostRuntime) sendRestartResult(failure error) {
	h.mu.Lock()
	c := h.client
	h.mu.Unlock()
	if c == nil {
		return
	}
	var payload []byte
	if failure != nil {
		payload = []byte(failure.Error())
	}
	if !c.send(RestartResult, payload) {
		h.log.Printf("could not send the restart outcome to the attached client: its queue is full")
	}
}
