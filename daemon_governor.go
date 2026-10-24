package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/dphaener/afk/internal/daemon"
	"github.com/dphaener/afk/internal/daemonconfig"
	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/governor"
	"github.com/dphaener/afk/internal/host"
	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/session"
	"github.com/dphaener/afk/internal/usagestate"
	"github.com/dphaener/afk/internal/usagetext"
)

// This file is the real side of the daemon's usage governor (PLAN §6.3 to
// §6.5): where the loop's usage verdict and login verdict come from, and how
// a session is stopped for a pause. The rules about what to do with a verdict
// are in internal/daemon; nothing here decides what happens to a task.

const (
	// usageProbeRetry is the least time between two usage probes. It only
	// matters after a probe that gave no reading: without it a failing probe
	// would be run on every pass, and each one may take usageProbeTimeout.
	usageProbeRetry = time.Minute
	// loginCheckEvery is how often the login is checked while it is fine or
	// unknown. A login ends rarely, and the check starts the agent's
	// command-line tool, so an hour is often enough (PLAN §6.5).
	loginCheckEvery = time.Hour
	// loginCheckTimeout bounds one login check. The status command answers
	// from local files in well under a second; twenty seconds covers a slow
	// start without letting a stuck process hold a pass.
	loginCheckTimeout = 20 * time.Second
	// stopExitWait bounds the polite stop: the dial and the one frame.
	stopExitWait = 2 * time.Second
	// stopForceWait is how long a forced stop waits for the host to go after
	// SIGTERM. The host's own ladder on its agent takes up to seven seconds
	// (five after SIGTERM, then a kill and two more; terminateChild in
	// internal/host/host.go), so ten covers a host that is doing its job.
	stopForceWait = 10 * time.Second
	// stopPollEvery is how often the forced stop looks whether the host has
	// gone.
	stopPollEvery = 100 * time.Millisecond
)

// usageSourceDeps is everything the daemon's usage source reaches outside
// for, so the whole of it runs in a table test with no CLI, no real clock and
// no real state folder.
type usageSourceDeps struct {
	Now func() time.Time
	// Dir is afk's per-machine state folder, where governor.json lives.
	Dir func() (string, error)
	// ReadUsage reads usage.json. The bool is false when there is no usable
	// reading (no file, or one that does not parse). The error is set only
	// when the file is there and cannot be read at all, and it names the
	// file.
	ReadUsage func() (usagestate.File, bool, error)
	// WriteUsage stores usage.json. Its error names the file.
	WriteUsage func(usagestate.File) error
	// Probe asks the agent's command-line tool for a new reading.
	Probe         func() (provider.UsageSnapshot, error)
	ReadGovernor  func(dir string) (daemonstate.GovernorRecord, bool)
	WriteGovernor func(dir string, r daemonstate.GovernorRecord) error
	Thresholds    governor.Thresholds
	ProbeInterval time.Duration
	// Provider is the provider's own name (its Name method). A reading stored
	// by another provider is not this account's usage.
	Provider string
	// Log may be nil.
	Log func(format string, args ...any)
}

// usageSource is the loop's Deps.Usage. It keeps two things in memory, and
// neither is state the loop depends on: both are rebuilt by the first call
// after a restart.
type usageSource struct {
	d usageSourceDeps
	// unsupported is set once the provider has said it cannot report usage.
	// That is a fact about the provider, not about the moment, so it is not
	// asked again. It has to be remembered here: the attempt is stamped in
	// governor.json before the probe, so without this the calls inside the
	// next minute would find no reading and no probe due, and answer "hold"
	// where the first call answered what on_unknown_usage says.
	unsupported bool
	// said is the last problem written to the log, so a problem that lasts is
	// logged once and not on every pass.
	said string
}

// view gives the governor's verdict from the latest usage reading, probing
// for a new one when the stored one is old enough.
//
// A call can block for as long as one probe (usageProbeTimeout), but at most
// once per probe_interval while probes succeed, and at most once a minute
// while they fail. The probe has the same limit `afk usage` gives it on
// purpose: a shorter one that the real tool missed would leave the daemon
// with no reading, holding every launch for ever.
//
// The order of the steps matters in two places. The attempt is saved before
// the probe runs, so a daemon killed in the middle of one does not probe
// again the moment it comes back. And nothing is probed while governor.json
// says the agent is logged out: the probe would fail, and the login source
// re-checks the login by itself (the loop asks it first on every pass).
//
// usage.json is written when a new reading came in, or when the decision's
// action or wake time differs from the one stored with the reading. The
// reason is deliberately not compared: a stale reading's sentence carries the
// reading's age in minutes, and comparing it would rewrite the file on every
// pass.
//
// A state folder or a usage.json that cannot be used at all is a hold with
// the reason saying which file, and never "not consulted": not consulted
// turns every usage rule off, and a daemon that cannot see usage must not run
// as if there were no limit.
func (s *usageSource) view() daemon.UsageView {
	d := s.d
	now := d.Now()
	if s.unsupported {
		return s.unsupportedView(now)
	}

	dir, err := d.Dir()
	if err != nil {
		return s.problem(fmt.Sprintf("afk's state folder cannot be found (%v), so afk is holding new work. Set XDG_STATE_HOME to an absolute path or make sure your home directory is known.", err))
	}
	stored, have, err := d.ReadUsage()
	if err != nil {
		return s.problem(fmt.Sprintf("The usage file cannot be read (%v), so afk is holding new work. Check the file's permissions.", err))
	}
	// A reading from another provider is another account's usage, and one
	// stamped in the future cannot be given an age. `afk usage` ignores both
	// the same way.
	if have && (stored.Provider != d.Provider || stored.At.After(now)) {
		have = false
	}

	rec, _ := d.ReadGovernor(dir)
	fresh := false
	if usageProbeDue(now, have, stored.At, rec, d.ProbeInterval) {
		rec.ProbeTriedAt = now.UTC().Format(time.RFC3339)
		if err := d.WriteGovernor(dir, rec); err != nil {
			// Without the saved attempt nothing would stop a failing probe
			// from running on every pass, so no probe is made.
			return s.problem(fmt.Sprintf("The file %s cannot be written (%v), so afk cannot check usage and is holding new work. Check that afk's state folder can be written to.", daemonstate.GovernorPath(dir), err))
		}
		snap, err := d.Probe()
		// The probe may have taken up to a minute.
		now = d.Now()
		switch {
		case errors.Is(err, provider.ErrUsageUnsupported):
			s.unsupported = true
			return s.unsupportedView(now)
		case err != nil:
			// The old reading is kept. Nothing more is needed to stay safe:
			// the governor holds by itself once that reading is stale.
			s.say(fmt.Sprintf("usage could not be read, so the last reading is kept: %v", err))
		default:
			if snap.At.IsZero() {
				snap.At = now
			}
			stored, have, fresh = usagestate.FromSnapshot(d.Provider, snap), true, true
			s.said = ""
		}
	}

	if !have {
		// Nothing is written: there is no reading to store, and a file with
		// only a decision in it would read as a reading with no windows.
		dec := governor.Decide(governor.Input{Now: now, Thresholds: d.Thresholds})
		return daemon.UsageView{Consulted: true, Action: dec.Action, Reason: dec.Reason}
	}

	snap := stored.Snapshot()
	dec := governor.Decide(governor.Input{Snapshot: &snap, Now: now, Thresholds: d.Thresholds})
	if fresh || usageDecisionChanged(stored.Governor, dec) {
		stored.Governor = usageDecisionRecord(dec, now)
		if err := d.WriteUsage(stored); err != nil {
			return s.problem(fmt.Sprintf("The usage file cannot be written (%v), so afk is holding new work. Check that afk's state folder can be written to.", err))
		}
	}
	return daemon.UsageView{
		Consulted: true,
		Action:    dec.Action,
		Reason:    dec.Reason,
		At:        snap.At,
		WakeAt:    dec.WakeAt,
		NextReset: usageNextReset(snap, now),
	}
}

// unsupportedView is the verdict for a provider that cannot report usage:
// what on_unknown_usage says. Nothing is stored, because there is no reading.
// At is the current time, since the verdict is as new as the call: a session
// paused under another provider's reading must not wait for a reading that
// will never come.
func (s *usageSource) unsupportedView(now time.Time) daemon.UsageView {
	dec := governor.Decide(governor.Input{Unsupported: true, Now: now, Thresholds: s.d.Thresholds})
	return daemon.UsageView{Consulted: true, Action: dec.Action, Reason: dec.Reason, At: now}
}

// problem is the hold returned when afk's own files cannot be used. The
// reason goes to the daemon's log as well, because the loop shows a fixed
// sentence for a hold and usage.json, where the governor's own sentence is
// kept, is the file that cannot be used.
func (s *usageSource) problem(reason string) daemon.UsageView {
	s.say(reason)
	return daemon.UsageView{Consulted: true, Action: governor.ActionHold, Reason: reason}
}

func (s *usageSource) say(msg string) {
	if msg == s.said {
		return
	}
	s.said = msg
	if s.d.Log != nil {
		s.d.Log("usage: %s", msg)
	}
}

// usageProbeDue reports whether a new usage reading should be asked for now.
// Pure, so each reason not to probe is a table row.
//
// An attempt stamped in the future (a clock that was set back) is ignored
// rather than obeyed, or it would switch probing off until that time came.
func usageProbeDue(now time.Time, have bool, readAt time.Time, rec daemonstate.GovernorRecord, interval time.Duration) bool {
	if rec.LoginKnown && !rec.LoggedIn {
		return false
	}
	if have && now.Sub(readAt) < interval {
		return false
	}
	if tried, err := time.Parse(time.RFC3339, rec.ProbeTriedAt); err == nil && !tried.After(now) && now.Sub(tried) < usageProbeRetry {
		return false
	}
	return true
}

// usageDecisionChanged reports whether a decision differs from the one stored
// with the reading in what the daemon acts on: the action and the wake time.
func usageDecisionChanged(stored *usagestate.Governor, dec governor.Decision) bool {
	if stored == nil || stored.Action != string(dec.Action) {
		return true
	}
	if stored.WakeAt == nil {
		return !dec.WakeAt.IsZero()
	}
	return !stored.WakeAt.Equal(dec.WakeAt)
}

// usageDecisionRecord is a decision in the shape usage.json keeps. A decision
// with no wake time gets no wake_at key (see usagestate.Governor).
func usageDecisionRecord(dec governor.Decision, now time.Time) *usagestate.Governor {
	g := &usagestate.Governor{Action: string(dec.Action), Reason: dec.Reason, At: now.UTC()}
	if !dec.WakeAt.IsZero() {
		t := dec.WakeAt.UTC()
		g.WakeAt = &t
	}
	return g
}

// usageNextReset is the earliest reset still ahead of now among the reading's
// windows, plus a minute, or the zero time when no window has one. It is the
// wake time for a session stopped by its rate-limit marker while the governor
// itself is not pausing.
//
// The earliest, and not the reset of the fullest window, because the marker
// does not say which limit was hit and the reading cannot be trusted to: it
// may be minutes old, and work since then can fill the short window while a
// longer one still stands higher in the reading. A real run showed the cost:
// a reading of session 10%, week 20% gave a wake time three days away for a
// limit that can only have been the session's. The wake time is only the
// first moment the governor is asked again, and a resume still needs a
// reading taken after the pause that allows work. So waking too early costs
// one usage reading and the session stays paused, while waking too late
// costs days and nothing shortens it.
//
// A reset at or before now is skipped: that window has already turned over,
// so it cannot be the one that ends this limit. The minute is the same margin
// the governor adds to its own wake time, so afk does not start again in the
// second the window turns over.
func usageNextReset(snap provider.UsageSnapshot, now time.Time) time.Time {
	var first time.Time
	for _, w := range snap.Windows {
		if !w.ResetsAt.After(now) {
			continue
		}
		if first.IsZero() || w.ResetsAt.Before(first) {
			first = w.ResetsAt
		}
	}
	if first.IsZero() {
		return time.Time{}
	}
	return first.Add(time.Minute)
}

// loginSourceDeps is the login source's outside world, for the same reason as
// usageSourceDeps.
type loginSourceDeps struct {
	Now           func() time.Time
	Dir           func() (string, error)
	ReadGovernor  func(dir string) (daemonstate.GovernorRecord, bool)
	WriteGovernor func(dir string, r daemonstate.GovernorRecord) error
	// Check asks the agent's command-line tool whether it is logged in.
	Check         func() (provider.LoginStatus, error)
	ProbeInterval time.Duration
	// Log may be nil.
	Log func(format string, args ...any)
}

// loginSource is the loop's Deps.Login. said is the last problem logged, as
// in usageSource.
type loginSource struct {
	d    loginSourceDeps
	said string
}

// view gives the last verdict on the login, checking again when that verdict
// is old enough (loginCheckDue).
//
// A provider that cannot report its login gives an unknown verdict, and the
// time is still saved so it is not asked on every pass. A check that fails
// for any other reason keeps the verdict it had and saves the time too: a
// check that could not run says nothing about the login, and turning it into
// "logged out" would raise a false alarm whenever the tool is slow.
//
// When the verdict cannot be saved it is still returned, and the check then
// runs again on the next pass. That costs one short command a pass until the
// state folder is writable again, and is said in the log once.
func (s *loginSource) view() daemon.LoginView {
	d := s.d
	now := d.Now()
	dir, err := d.Dir()
	if err != nil {
		s.say(fmt.Sprintf("afk's state folder cannot be found (%v), so the login is not checked", err))
		return daemon.LoginView{}
	}
	rec, _ := d.ReadGovernor(dir)
	if loginCheckDue(now, rec, d.ProbeInterval) {
		st, err := d.Check()
		switch {
		case errors.Is(err, provider.ErrLoginUnsupported):
			rec.LoginKnown, rec.LoggedIn = false, false
		case err != nil:
			s.say(fmt.Sprintf("the login could not be checked, so the last answer is kept: %v", err))
		default:
			rec.LoginKnown, rec.LoggedIn = true, st.LoggedIn
		}
		rec.LoginCheckedAt = now.UTC().Format(time.RFC3339)
		if werr := d.WriteGovernor(dir, rec); werr != nil {
			s.say(fmt.Sprintf("the login check could not be saved to %s, so it runs again on every look: %v", daemonstate.GovernorPath(dir), werr))
		} else if err == nil || errors.Is(err, provider.ErrLoginUnsupported) {
			s.said = ""
		}
	}
	return daemon.LoginView{Known: rec.LoginKnown, LoggedIn: rec.LoggedIn}
}

func (s *loginSource) say(msg string) {
	if msg == s.said {
		return
	}
	s.said = msg
	if s.d.Log != nil {
		s.d.Log("login: %s", msg)
	}
}

// loginCheckDue reports whether the login should be checked now. Pure.
//
// It is checked when it never was, every hour, and every probe_interval while
// the last verdict is "logged out": the owner is then expected to log in
// again, and the build should pick up within minutes of that and not within
// the hour. A check time that cannot be read, or lies in the future, counts
// as never checked.
func loginCheckDue(now time.Time, rec daemonstate.GovernorRecord, probeInterval time.Duration) bool {
	checked, err := time.Parse(time.RFC3339, rec.LoginCheckedAt)
	if err != nil || checked.After(now) {
		return true
	}
	age := now.Sub(checked)
	if age >= loginCheckEvery {
		return true
	}
	return rec.LoginKnown && !rec.LoggedIn && age >= probeInterval
}

// realUsageSource wires the usage source to the machine: usage.json and
// governor.json in afk's state folder, and the provider's own probe with the
// runner and the limit `afk usage` uses, run from the state folder for the
// reason given on realUsageRunner.
func realUsageSource(p provider.Provider, st usagestate.Deps, g daemonconfig.Governor, now func() time.Time, logf func(string, ...any)) *usageSource {
	run := realUsageRunner(st)
	return &usageSource{d: usageSourceDeps{
		Now:        now,
		Dir:        func() (string, error) { return usagestate.Dir(st) },
		ReadUsage:  func() (usagestate.File, bool, error) { return readUsageFile(st) },
		WriteUsage: func(f usagestate.File) error { return usagestate.Write(st, f) },
		Probe: func() (provider.UsageSnapshot, error) {
			ctx, cancel := context.WithTimeout(context.Background(), usageProbeTimeout)
			defer cancel()
			return p.Usage(ctx, provider.UsageDeps{Run: run, Now: now})
		},
		ReadGovernor:  daemonstate.ReadGovernor,
		WriteGovernor: daemonstate.WriteGovernor,
		Thresholds:    g.Thresholds(),
		ProbeInterval: g.ProbeInterval,
		Provider:      p.Name(),
		Log:           logf,
	}}
}

// readUsageFile is usagestate.Read with one more answer. Read says only
// "no reading", which is right for a file that is missing or half written (a
// probe replaces it). A file that is there and cannot be opened is a
// different thing: the daemon then cannot see what `afk usage` or another
// run stored, and the owner has to be told which file to look at.
func readUsageFile(st usagestate.Deps) (usagestate.File, bool, error) {
	path, err := usagestate.Path(st)
	if err != nil {
		return usagestate.File{}, false, err
	}
	read := st.ReadFile
	if read == nil {
		read = os.ReadFile
	}
	if _, err := read(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return usagestate.File{}, false, err
	}
	f, ok := usagestate.Read(st)
	return f, ok, nil
}

// realLoginSource wires the login source to the machine.
func realLoginSource(p provider.Provider, st usagestate.Deps, g daemonconfig.Governor, now func() time.Time, logf func(string, ...any)) *loginSource {
	run := realUsageRunner(st)
	return &loginSource{d: loginSourceDeps{
		Now:           now,
		Dir:           func() (string, error) { return usagestate.Dir(st) },
		ReadGovernor:  daemonstate.ReadGovernor,
		WriteGovernor: daemonstate.WriteGovernor,
		Check: func() (provider.LoginStatus, error) {
			ctx, cancel := context.WithTimeout(context.Background(), loginCheckTimeout)
			defer cancel()
			return p.Login(ctx, provider.LoginDeps{Run: run})
		},
		ProbeInterval: g.ProbeInterval,
		Log:           logf,
	}}
}

// stopDeps is what stopping a session touches, so the test sends no signal
// and reads no real /proc.
type stopDeps struct {
	// RequestExit sends the host its Exit frame (host.RequestExit).
	RequestExit func(worktree, name string, wait time.Duration) error
	ReadMeta    func(worktree, name string) (host.Meta, error)
	// Cmdline is the command line of a running process, one argument per
	// element. An error means it could not be read.
	Cmdline func(pid int) ([]string, error)
	Signal  func(pid int, sig syscall.Signal) error
	Alive   func(pid int) bool
	Sleep   func(time.Duration)
}

func realStopDeps() stopDeps {
	return stopDeps{
		RequestExit: func(worktree, name string, wait time.Duration) error {
			rt, err := session.RuntimeFor(worktree, name)
			if err != nil {
				return err
			}
			return rt.Exit(worktree, name, wait)
		},
		ReadMeta: host.ReadMeta,
		Cmdline:  procCmdline,
		Signal:   syscall.Kill,
		Alive:    host.Alive,
		Sleep:    time.Sleep,
	}
}

// procCmdline reads a process's command line from /proc, where the kernel
// keeps the arguments separated by NUL bytes. On a system with no /proc the
// read fails, and the caller then signals nothing.
func procCmdline(pid int) ([]string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimRight(string(data), "\x00"), "\x00"), nil
}

// daemonStopSession is the loop's Deps.StopSession.
//
// Without force it is the polite stop: one Exit frame to the session's host,
// which ends its agent and then itself.
//
// With force it sends the host SIGTERM and waits up to stopForceWait for it
// to go. It never sends SIGKILL, and that is the point of it. On SIGTERM the
// host takes its agent down by its own ladder and only then leaves. A host
// killed in the middle of that leaves the agent running in a terminal session
// of its own, where afk can no longer reach it: still spending, with nothing
// left to stop it, which is the one thing a pause must not cause. `afk clean`
// may kill a host two seconds after SIGTERM because it removes the worktree
// next; the daemon keeps the worktree, so it waits instead. A host that will
// not go is the loop's to handle: it asks again on later passes and then
// tells the owner the session could not be stopped. Until then each of those
// passes may wait here for the whole stopForceWait.
//
// Before any signal the pid is checked to still be this session's host. The
// liveness check everything else relies on (host.Alive) is a bare signal 0,
// and it is documented as fooled by a reused pid: a host that crashed leaves
// host.json behind, and the number in it may by now belong to any process of
// this user. So the process's command line must hold afk's own host argument
// and this session's name. When it holds something else, or cannot be read
// (no /proc on this system), nothing is sent and an error is returned. A
// forced stop therefore works on Linux only; elsewhere a session that ignores
// the polite stop is reported to the owner.
func daemonStopSession(d stopDeps) func(worktree, session string, force bool) error {
	return func(worktree, session string, force bool) error {
		if !force {
			return d.RequestExit(worktree, session, stopExitWait)
		}
		meta, err := d.ReadMeta(worktree, session)
		if err != nil {
			return fmt.Errorf("session %s cannot be stopped by signal: its host record cannot be read: %w", session, err)
		}
		if meta.PID <= 0 {
			return fmt.Errorf("session %s cannot be stopped by signal: its host record holds no process id", session)
		}
		if !d.Alive(meta.PID) {
			return nil
		}
		args, err := d.Cmdline(meta.PID)
		if err != nil {
			return fmt.Errorf("session %s was not signalled: process %d could not be confirmed to be its host: %w", session, meta.PID, err)
		}
		if !isSessionHost(args, session) {
			return fmt.Errorf("session %s was not signalled: process %d is not its host any more (the host record is left over from a host that crashed)", session, meta.PID)
		}
		if err := d.Signal(meta.PID, syscall.SIGTERM); err != nil && d.Alive(meta.PID) {
			return fmt.Errorf("session %s: stopping its host (pid %d): %w", session, meta.PID, err)
		}
		for waited := time.Duration(0); waited < stopForceWait; waited += stopPollEvery {
			if !d.Alive(meta.PID) {
				return nil
			}
			d.Sleep(stopPollEvery)
		}
		if !d.Alive(meta.PID) {
			return nil
		}
		return fmt.Errorf("session %s: its host (pid %d) is still running %s after it was told to stop", session, meta.PID, stopForceWait)
	}
}

// isSessionHost reports whether a command line is afk's host for this
// session. Pure. The host is started as "afk _host --name <session> ..."
// (hostArgs in internal/session/create.go); the program's own name is not
// looked at, because the binary may be installed under any path.
func isSessionHost(args []string, session string) bool {
	if session == "" {
		return false
	}
	for i, a := range args {
		if a != "_host" {
			continue
		}
		for _, b := range args[i+1:] {
			if b == session || b == "--name="+session {
				return true
			}
		}
		return false
	}
	return false
}

// usageStatusLine is the one machine-wide usage line of `afk daemon status`,
// or "" when usage.json holds no decision. Pure.
//
// It shows the decision as it was stored and does not work it out again: the
// limits may have changed since, and the line is about what the daemon is
// acting on. The time it was made is shown because the file outlives the
// daemon, and an old decision must not read as the present.
func usageStatusLine(g *usagestate.Governor, loc *time.Location) string {
	if g == nil || g.Action == "" {
		return ""
	}
	if loc == nil {
		loc = time.UTC
	}
	const layout = "Mon 2 Jan 15:04"
	lead := usagetext.Lead(g, loc)
	line := "Usage: " + lead + "."
	if r := strings.TrimSpace(g.Reason); r != "" {
		line += " " + r
	}
	if !g.At.IsZero() {
		line += " (as of " + g.At.In(loc).Format(layout) + ")"
	}
	return line
}
