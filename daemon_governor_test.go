package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/governor"
	"github.com/dphaener/afk/internal/host"
	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/usagestate"
)

// govNow is the clock of every test in this file.
var govNow = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

func govStamp(ago time.Duration) string { return govNow.Add(-ago).Format(time.RFC3339) }

// govWindows is a three-window account. The session window resets first, and
// in most rows it is the least full of the three; the model window has no
// reset time. That is what makes the NextReset checks mean something: the
// answer is the earliest reset, whichever window is fullest.
func govWindows(session, week, model float64) []provider.Window {
	return []provider.Window{
		{Kind: provider.WindowSession, UsedPct: session, ResetsAt: govNow.Add(2 * time.Hour)},
		{Kind: provider.WindowWeekly, UsedPct: week, ResetsAt: govNow.Add(50 * time.Hour)},
		{Kind: provider.WindowWeeklyModel, UsedPct: model, Scope: "Fable"},
	}
}

// govStored is a usage.json of the given age for the given provider, with a
// stored decision when action is not empty.
func govStored(prov string, age time.Duration, windows []provider.Window, action string) *usagestate.File {
	f := usagestate.FromSnapshot(prov, provider.UsageSnapshot{At: govNow.Add(-age), Windows: windows})
	if action != "" {
		f.Governor = &usagestate.Governor{Action: action, Reason: "stored earlier", At: govNow.Add(-age)}
	}
	return &f
}

// govRig is a usage or login source over a temp state folder: real files, a
// fixed clock and a scripted probe. No command is run.
type govRig struct {
	t         *testing.T
	stateHome string
	st        usagestate.Deps
	calls     int
	logs      []string
}

func newGovRig(t *testing.T) *govRig {
	t.Helper()
	r := &govRig{t: t, stateHome: t.TempDir()}
	r.st = usagestate.Deps{
		Getenv: func(k string) string {
			if k == "XDG_STATE_HOME" {
				return r.stateHome
			}
			return ""
		},
		HomeDir: func() (string, error) { return "", errors.New("no home in tests") },
	}
	return r
}

func (r *govRig) dir() string       { return filepath.Join(r.stateHome, "afk") }
func (r *govRig) usagePath() string { return filepath.Join(r.dir(), "usage.json") }
func (r *govRig) log(format string, args ...any) {
	r.logs = append(r.logs, format)
}

func (r *govRig) usageDeps(snap provider.UsageSnapshot, probeErr error) usageSourceDeps {
	return usageSourceDeps{
		Now:        func() time.Time { return govNow },
		Dir:        func() (string, error) { return usagestate.Dir(r.st) },
		ReadUsage:  func() (usagestate.File, bool, error) { return readUsageFile(r.st) },
		WriteUsage: func(f usagestate.File) error { return usagestate.Write(r.st, f) },
		Probe: func() (provider.UsageSnapshot, error) {
			r.calls++
			return snap, probeErr
		},
		ReadGovernor:  daemonstate.ReadGovernor,
		WriteGovernor: daemonstate.WriteGovernor,
		Thresholds:    governor.DefaultThresholds(),
		ProbeInterval: governor.DefaultProbeInterval,
		Provider:      "fake",
		Log:           r.log,
	}
}

// TestUsageNextReset covers the wake time handed to the loop for a session
// stopped by its rate-limit marker.
//
// Load-bearing rows:
//   - "the real check": session 10% resetting in two hours, week 20% resetting
//     in three days. Choosing the fullest window gave a three-day wait for a
//     limit that can only have been the session's.
//   - "a fuller window that resets later does not win" and "order does not
//     matter": the choice is by time alone.
//   - the "past" and "exactly now" rows: a window that has already turned
//     over cannot end this limit, and taking it would hide a later reset
//     behind the loop's one-hour fallback.
func TestUsageNextReset(t *testing.T) {
	in := func(d time.Duration) time.Time { return govNow.Add(d) }
	win := func(kind string, pct float64, reset time.Time) provider.Window {
		return provider.Window{Kind: kind, UsedPct: pct, ResetsAt: reset}
	}
	tests := []struct {
		name    string
		windows []provider.Window
		want    time.Time
	}{
		{
			name: "the real check: session 10% in two hours, week 20% in three days",
			windows: []provider.Window{
				win(provider.WindowSession, 10, in(2*time.Hour)),
				win(provider.WindowWeekly, 20, in(72*time.Hour)),
			},
			want: in(2*time.Hour + time.Minute),
		},
		{
			name: "a fuller window that resets later does not win",
			windows: []provider.Window{
				win(provider.WindowSession, 5, in(time.Hour)),
				win(provider.WindowWeekly, 99, in(50*time.Hour)),
			},
			want: in(time.Hour + time.Minute),
		},
		{
			name: "order does not matter",
			windows: []provider.Window{
				win(provider.WindowWeekly, 20, in(50*time.Hour)),
				win(provider.WindowWeeklyModel, 30, in(40*time.Hour)),
				win(provider.WindowSession, 10, in(3*time.Hour)),
			},
			want: in(3*time.Hour + time.Minute),
		},
		{
			name: "a reset already past is skipped",
			windows: []provider.Window{
				win(provider.WindowSession, 10, in(-5*time.Minute)),
				win(provider.WindowWeekly, 20, in(50*time.Hour)),
			},
			want: in(50*time.Hour + time.Minute),
		},
		{
			name: "a reset exactly now is not ahead and is skipped",
			windows: []provider.Window{
				win(provider.WindowSession, 10, in(0)),
				win(provider.WindowWeekly, 20, in(50*time.Hour)),
			},
			want: in(50*time.Hour + time.Minute),
		},
		{
			name: "a window with no reset time is skipped",
			windows: []provider.Window{
				win(provider.WindowWeeklyModel, 80, time.Time{}),
				win(provider.WindowWeekly, 20, in(50*time.Hour)),
			},
			want: in(50*time.Hour + time.Minute),
		},
		{
			name: "every reset past gives none",
			windows: []provider.Window{
				win(provider.WindowSession, 10, in(-time.Hour)),
				win(provider.WindowWeekly, 20, in(-time.Second)),
			},
		},
		{
			name:    "no window with a reset gives none",
			windows: []provider.Window{win(provider.WindowSession, 60, time.Time{})},
		},
		{name: "no windows gives none"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := usageNextReset(provider.UsageSnapshot{At: govNow, Windows: tt.windows}, govNow)
			if !got.Equal(tt.want) {
				t.Errorf("usageNextReset = %s, want %s", got, tt.want)
			}
		})
	}
}

// TestGovernorUsageSource covers the daemon's usage source.
//
// Load-bearing rows:
//   - "fresh reading is reused" and "probe failure is not retried within a
//     minute": the probe starts the agent's command-line tool and may take a
//     minute, so a pass every few seconds must not run it.
//   - "no probe while logged out": the probe would fail, and the login source
//     re-checks by itself.
//   - "unsupported": nothing is written and the reading's time is now, so
//     on_unknown_usage decides and a paused session is not left waiting for
//     a reading that never comes.
//   - "another provider's reading is ignored": that is another account's
//     usage, and it must not be overwritten with nothing when the probe fails.
//   - the four "written" rows: usage.json is rewritten for a new reading, a
//     new action or a new wake time, and never for a new sentence alone, or a
//     stale hold (whose sentence holds the age in minutes) would rewrite the
//     file on every pass.
//   - the four "cannot" rows: a state folder or file that cannot be used is a
//     consulted hold naming the file, never "not consulted", which would turn
//     every usage rule off.
func TestGovernorUsageSource(t *testing.T) {
	probeFail := errors.New("the tool did not answer")
	hold := governor.DefaultThresholds()
	hold.OnUnknownUsage = governor.UnknownHold
	lowLaunch := governor.DefaultThresholds()
	lowLaunch.LaunchBelow = 25
	loggedOut := &daemonstate.GovernorRecord{LoginCheckedAt: govStamp(time.Minute), LoginKnown: true, LoggedIn: false}
	pauseWake := govNow.Add(2*time.Hour + time.Minute)

	tests := []struct {
		name   string
		stored *usagestate.File
		rec    *daemonstate.GovernorRecord
		// probe is the reading the fake probe gives; probeErr its error.
		probe    provider.UsageSnapshot
		probeErr error
		th       *governor.Thresholds
		// mutate changes the deps after they are built, for the failure rows.
		mutate func(r *govRig, d *usageSourceDeps)

		wantCalls  int
		wantAction governor.Action
		// wantAt is the view's reading time; the zero time means "no reading".
		wantAt        time.Time
		wantWake      time.Time
		wantNextReset time.Time
		wantReason    []string
		// wantWritten: usage.json differs from what was there before the call.
		wantWritten bool
		// wantStored is the action stored in usage.json afterwards; "" means
		// the file must hold no decision (or not be there).
		wantStored string
		// wantTried: probe_tried_at was stamped with this call's time.
		wantTried bool
		wantLog   bool
	}{
		{
			name:       "fresh reading is reused",
			stored:     govStored("fake", 2*time.Minute, govWindows(10, 20, 30), "run"),
			probe:      provider.UsageSnapshot{At: govNow, Windows: govWindows(99, 99, 99)},
			wantAction: governor.ActionRun, wantAt: govNow.Add(-2 * time.Minute),
			wantNextReset: govNow.Add(2*time.Hour + time.Minute), wantStored: "run",
		},
		{
			name:      "old reading is probed again",
			stored:    govStored("fake", 6*time.Minute, govWindows(10, 20, 30), "run"),
			probe:     provider.UsageSnapshot{At: govNow, Windows: govWindows(30, 40, 50)},
			wantCalls: 1, wantAction: governor.ActionRun, wantAt: govNow,
			wantNextReset: govNow.Add(2*time.Hour + time.Minute),
			wantWritten:   true, wantStored: "run", wantTried: true,
		},
		{
			name:      "no reading at all is probed",
			probe:     provider.UsageSnapshot{At: govNow, Windows: govWindows(30, 40, 50)},
			wantCalls: 1, wantAction: governor.ActionRun, wantAt: govNow,
			wantNextReset: govNow.Add(2*time.Hour + time.Minute),
			wantWritten:   true, wantStored: "run", wantTried: true,
		},
		{
			name:      "probe failure keeps the old reading",
			stored:    govStored("fake", 6*time.Minute, govWindows(10, 20, 30), "run"),
			probeErr:  probeFail,
			wantCalls: 1, wantAction: governor.ActionRun, wantAt: govNow.Add(-6 * time.Minute),
			wantNextReset: govNow.Add(2*time.Hour + time.Minute),
			wantStored:    "run", wantTried: true, wantLog: true,
		},
		{
			name:       "probe failure is not retried within a minute",
			stored:     govStored("fake", 6*time.Minute, govWindows(10, 20, 30), "run"),
			rec:        &daemonstate.GovernorRecord{ProbeTriedAt: govStamp(30 * time.Second)},
			probe:      provider.UsageSnapshot{At: govNow, Windows: govWindows(99, 99, 99)},
			wantAction: governor.ActionRun, wantAt: govNow.Add(-6 * time.Minute),
			wantNextReset: govNow.Add(2*time.Hour + time.Minute), wantStored: "run",
		},
		{
			name:      "probe failure is retried after a minute",
			stored:    govStored("fake", 6*time.Minute, govWindows(10, 20, 30), "run"),
			rec:       &daemonstate.GovernorRecord{ProbeTriedAt: govStamp(time.Minute)},
			probe:     provider.UsageSnapshot{At: govNow, Windows: govWindows(30, 40, 50)},
			wantCalls: 1, wantAction: governor.ActionRun, wantAt: govNow,
			wantNextReset: govNow.Add(2*time.Hour + time.Minute),
			wantWritten:   true, wantStored: "run", wantTried: true,
		},
		{
			name:      "an attempt stamped in the future does not switch probing off",
			stored:    govStored("fake", 6*time.Minute, govWindows(10, 20, 30), "run"),
			rec:       &daemonstate.GovernorRecord{ProbeTriedAt: govNow.Add(3 * time.Hour).Format(time.RFC3339)},
			probe:     provider.UsageSnapshot{At: govNow, Windows: govWindows(30, 40, 50)},
			wantCalls: 1, wantAction: governor.ActionRun, wantAt: govNow,
			wantNextReset: govNow.Add(2*time.Hour + time.Minute),
			wantWritten:   true, wantStored: "run", wantTried: true,
		},
		{
			name:       "no probe while logged out",
			stored:     govStored("fake", 6*time.Minute, govWindows(10, 20, 30), "run"),
			rec:        loggedOut,
			probe:      provider.UsageSnapshot{At: govNow, Windows: govWindows(99, 99, 99)},
			wantAction: governor.ActionRun, wantAt: govNow.Add(-6 * time.Minute),
			wantNextReset: govNow.Add(2*time.Hour + time.Minute), wantStored: "run",
		},
		{
			name:       "no probe while logged out, and with no reading that is a hold",
			rec:        loggedOut,
			probe:      provider.UsageSnapshot{At: govNow, Windows: govWindows(10, 20, 30)},
			wantAction: governor.ActionHold, wantReason: []string{"has not been read yet"},
		},
		{
			name:      "an unknown login does not stop the probe",
			stored:    govStored("fake", 6*time.Minute, govWindows(10, 20, 30), "run"),
			rec:       &daemonstate.GovernorRecord{LoginCheckedAt: govStamp(time.Minute), LoginKnown: false, LoggedIn: false},
			probe:     provider.UsageSnapshot{At: govNow, Windows: govWindows(30, 40, 50)},
			wantCalls: 1, wantAction: governor.ActionRun, wantAt: govNow,
			wantNextReset: govNow.Add(2*time.Hour + time.Minute),
			wantWritten:   true, wantStored: "run", wantTried: true,
		},
		{
			name:      "unsupported runs by default and writes nothing",
			probeErr:  provider.ErrUsageUnsupported,
			wantCalls: 1, wantAction: governor.ActionRun, wantAt: govNow, wantTried: true,
			wantReason: []string{"cannot be read for this tool"},
		},
		{
			name:      "unsupported holds when the config says so",
			probeErr:  provider.ErrUsageUnsupported,
			th:        &hold,
			wantCalls: 1, wantAction: governor.ActionHold, wantAt: govNow, wantTried: true,
		},
		{
			name:      "another provider's reading is ignored",
			stored:    govStored("other", time.Minute, govWindows(99, 99, 99), "pause"),
			probe:     provider.UsageSnapshot{At: govNow, Windows: govWindows(30, 40, 50)},
			wantCalls: 1, wantAction: governor.ActionRun, wantAt: govNow,
			wantNextReset: govNow.Add(2*time.Hour + time.Minute),
			wantWritten:   true, wantStored: "run", wantTried: true,
		},
		{
			name:      "another provider's reading is ignored and left alone when the probe fails",
			stored:    govStored("other", time.Minute, govWindows(99, 99, 99), "pause"),
			probeErr:  probeFail,
			wantCalls: 1, wantAction: governor.ActionHold, wantReason: []string{"has not been read yet"},
			wantStored: "pause", wantTried: true, wantLog: true,
		},
		{
			name:      "a reading stamped in the future is ignored",
			stored:    govStored("fake", -3*time.Hour, govWindows(10, 20, 30), "run"),
			probe:     provider.UsageSnapshot{At: govNow, Windows: govWindows(30, 40, 50)},
			wantCalls: 1, wantAction: governor.ActionRun, wantAt: govNow,
			wantNextReset: govNow.Add(2*time.Hour + time.Minute),
			wantWritten:   true, wantStored: "run", wantTried: true,
		},
		{
			name:       "written: a reused reading with no stored decision gets one",
			stored:     govStored("fake", 2*time.Minute, govWindows(10, 20, 30), ""),
			wantAction: governor.ActionRun, wantAt: govNow.Add(-2 * time.Minute),
			wantNextReset: govNow.Add(2*time.Hour + time.Minute),
			wantWritten:   true, wantStored: "run",
		},
		{
			name:       "written: the action changed with no new reading",
			stored:     govStored("fake", 40*time.Minute, govWindows(10, 20, 30), "run"),
			rec:        loggedOut,
			wantAction: governor.ActionHold, wantAt: govNow.Add(-40 * time.Minute),
			wantNextReset: govNow.Add(2*time.Hour + time.Minute),
			wantReason:    []string{"40 minutes old"},
			wantWritten:   true, wantStored: "hold",
		},
		{
			name:       "not written: the same action with a new sentence",
			stored:     govStored("fake", 40*time.Minute, govWindows(10, 20, 30), "hold"),
			rec:        loggedOut,
			wantAction: governor.ActionHold, wantAt: govNow.Add(-40 * time.Minute),
			wantNextReset: govNow.Add(2*time.Hour + time.Minute),
			wantReason:    []string{"40 minutes old"},
			wantStored:    "hold",
		},
		{
			name: "written: the same action with a new wake time",
			stored: func() *usagestate.File {
				f := govStored("fake", 2*time.Minute, govWindows(95, 20, 30), "pause")
				old := govNow.Add(time.Hour)
				f.Governor.WakeAt = &old
				return f
			}(),
			wantAction: governor.ActionPause, wantAt: govNow.Add(-2 * time.Minute),
			wantWake: pauseWake, wantNextReset: pauseWake,
			wantWritten: true, wantStored: "pause",
		},
		{
			name: "not written: a pause with the same wake time",
			stored: func() *usagestate.File {
				f := govStored("fake", 2*time.Minute, govWindows(95, 20, 30), "pause")
				f.Governor.WakeAt = &pauseWake
				return f
			}(),
			wantAction: governor.ActionPause, wantAt: govNow.Add(-2 * time.Minute),
			wantWake: pauseWake, wantNextReset: pauseWake, wantStored: "pause",
		},
		{
			name:      "the configured limits decide",
			probe:     provider.UsageSnapshot{At: govNow, Windows: govWindows(30, 40, 50)},
			th:        &lowLaunch,
			wantCalls: 1, wantAction: governor.ActionNoLaunch, wantAt: govNow,
			wantNextReset: govNow.Add(2*time.Hour + time.Minute),
			wantReason:    []string{"limit of 25%"},
			wantWritten:   true, wantStored: "no_launch", wantTried: true,
		},
		{
			// The reading from the real check: the week is fuller, and its
			// reset is three days away, but the session's comes first.
			name: "next reset is the earliest reset ahead, not the fullest window's",
			stored: func() *usagestate.File {
				f := usagestate.FromSnapshot("fake", provider.UsageSnapshot{At: govNow.Add(-time.Minute), Windows: []provider.Window{
					{Kind: provider.WindowSession, UsedPct: 10, ResetsAt: govNow.Add(2 * time.Hour)},
					{Kind: provider.WindowWeekly, UsedPct: 20, ResetsAt: govNow.Add(72 * time.Hour)},
					{Kind: provider.WindowWeeklyModel, UsedPct: 80, Scope: "Fable"},
				}})
				f.Governor = &usagestate.Governor{Action: "run", Reason: "stored earlier", At: govNow.Add(-time.Minute)}
				return &f
			}(),
			wantAction: governor.ActionRun, wantAt: govNow.Add(-time.Minute),
			wantNextReset: govNow.Add(2*time.Hour + time.Minute), wantStored: "run",
		},
		{
			name: "no window with a reset means no next reset",
			stored: func() *usagestate.File {
				f := usagestate.FromSnapshot("fake", provider.UsageSnapshot{At: govNow.Add(-time.Minute), Windows: []provider.Window{
					{Kind: provider.WindowSession, UsedPct: 60},
				}})
				f.Governor = &usagestate.Governor{Action: "run", Reason: "stored earlier", At: govNow.Add(-time.Minute)}
				return &f
			}(),
			wantAction: governor.ActionRun, wantAt: govNow.Add(-time.Minute), wantStored: "run",
		},
		{
			name:   "cannot find the state folder",
			stored: govStored("fake", 2*time.Minute, govWindows(10, 20, 30), "run"),
			mutate: func(r *govRig, d *usageSourceDeps) {
				d.Dir = func() (string, error) { return "", errors.New("resolving home directory: no home") }
			},
			wantAction: governor.ActionHold, wantReason: []string{"state folder cannot be found", "no home", "XDG_STATE_HOME"},
			wantStored: "run", wantLog: true,
		},
		{
			name:   "cannot read usage.json",
			stored: govStored("fake", 2*time.Minute, govWindows(10, 20, 30), "run"),
			mutate: func(r *govRig, d *usageSourceDeps) {
				st := r.st
				st.ReadFile = func(p string) ([]byte, error) {
					return nil, &os.PathError{Op: "open", Path: p, Err: syscall.EACCES}
				}
				d.ReadUsage = func() (usagestate.File, bool, error) { return readUsageFile(st) }
			},
			wantAction: governor.ActionHold, wantReason: []string{"cannot be read", "usage.json", "permission denied"},
			wantStored: "run", wantLog: true,
		},
		{
			name:  "cannot write usage.json",
			probe: provider.UsageSnapshot{At: govNow, Windows: govWindows(30, 40, 50)},
			mutate: func(r *govRig, d *usageSourceDeps) {
				st := r.st
				st.WriteFile = func(p string, _ []byte, _ os.FileMode) error {
					return &os.PathError{Op: "open", Path: p, Err: syscall.EROFS}
				}
				d.WriteUsage = func(f usagestate.File) error { return usagestate.Write(st, f) }
			},
			wantCalls: 1, wantAction: governor.ActionHold, wantReason: []string{"cannot be written", "usage.json", "read-only"},
			wantTried: true, wantLog: true,
		},
		{
			name:   "cannot write governor.json, so no probe is made",
			stored: govStored("fake", 6*time.Minute, govWindows(10, 20, 30), "run"),
			probe:  provider.UsageSnapshot{At: govNow, Windows: govWindows(30, 40, 50)},
			mutate: func(r *govRig, d *usageSourceDeps) {
				d.WriteGovernor = func(string, daemonstate.GovernorRecord) error { return errors.New("disk full") }
			},
			wantAction: governor.ActionHold, wantReason: []string{"governor.json", "cannot be written", "disk full"},
			wantStored: "run", wantLog: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newGovRig(t)
			if tt.stored != nil {
				if err := usagestate.Write(r.st, *tt.stored); err != nil {
					t.Fatal(err)
				}
			}
			if tt.rec != nil {
				if err := daemonstate.WriteGovernor(r.dir(), *tt.rec); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(r.usagePath())
			d := r.usageDeps(tt.probe, tt.probeErr)
			if tt.th != nil {
				d.Thresholds = *tt.th
			}
			if tt.mutate != nil {
				tt.mutate(r, &d)
			}
			got := (&usageSource{d: d}).view()

			if !got.Consulted {
				t.Error("Consulted is false; a usage source must never answer unconsulted")
			}
			if r.calls != tt.wantCalls {
				t.Errorf("probe calls = %d, want %d", r.calls, tt.wantCalls)
			}
			if got.Action != tt.wantAction {
				t.Errorf("Action = %q, want %q (reason %q)", got.Action, tt.wantAction, got.Reason)
			}
			if !got.At.Equal(tt.wantAt) {
				t.Errorf("At = %s, want %s", got.At, tt.wantAt)
			}
			if !got.WakeAt.Equal(tt.wantWake) {
				t.Errorf("WakeAt = %s, want %s", got.WakeAt, tt.wantWake)
			}
			if !got.NextReset.Equal(tt.wantNextReset) {
				t.Errorf("NextReset = %s, want %s", got.NextReset, tt.wantNextReset)
			}
			if got.Reason == "" {
				t.Error("Reason is empty")
			}
			for _, w := range tt.wantReason {
				if !strings.Contains(got.Reason, w) {
					t.Errorf("Reason lacks %q: %s", w, got.Reason)
				}
			}
			after, _ := os.ReadFile(r.usagePath())
			if written := !bytes.Equal(before, after); written != tt.wantWritten {
				t.Errorf("usage.json rewritten = %v, want %v\nbefore: %s\nafter: %s", written, tt.wantWritten, before, after)
			}
			stored := ""
			if f, ok := usagestate.Read(r.st); ok && f.Governor != nil {
				stored = f.Governor.Action
				if tt.wantWritten && (f.Governor.Reason != got.Reason || !f.Governor.At.Equal(govNow)) {
					t.Errorf("stored decision = %+v, want this call's reason, made at %s", *f.Governor, govNow)
				}
				if tt.wantWritten && (f.Governor.WakeAt == nil) != got.WakeAt.IsZero() {
					t.Errorf("stored wake_at = %v, view WakeAt = %s", f.Governor.WakeAt, got.WakeAt)
				}
			}
			if stored != tt.wantStored {
				t.Errorf("stored action = %q, want %q", stored, tt.wantStored)
			}
			rec, _ := daemonstate.ReadGovernor(r.dir())
			if tried := rec.ProbeTriedAt == govNow.Format(time.RFC3339); tried != tt.wantTried {
				t.Errorf("probe_tried_at = %q, stamped now = %v, want %v", rec.ProbeTriedAt, tried, tt.wantTried)
			}
			if tt.rec != nil && (rec.LoginCheckedAt != tt.rec.LoginCheckedAt || rec.LoginKnown != tt.rec.LoginKnown || rec.LoggedIn != tt.rec.LoggedIn) {
				t.Errorf("the login half of governor.json changed: %+v, was %+v", rec, *tt.rec)
			}
			if logged := len(r.logs) > 0; logged != tt.wantLog {
				t.Errorf("logged = %v (%v), want %v", logged, r.logs, tt.wantLog)
			}
		})
	}
}

// TestGovernorUsageSourceOverTime runs one source through several calls,
// which the table cannot: what a call leaves behind is what the next one
// reads.
//
// Load-bearing: the unsupported run (the second call must give the same
// answer without asking again; from the files alone it would find no reading
// and no probe due, and say hold), and the failing run (one probe and one log
// line across calls inside the minute, then one more of each).
func TestGovernorUsageSourceOverTime(t *testing.T) {
	t.Run("unsupported is asked once", func(t *testing.T) {
		r := newGovRig(t)
		s := &usageSource{d: r.usageDeps(provider.UsageSnapshot{}, provider.ErrUsageUnsupported)}
		for i := 0; i < 3; i++ {
			if got := s.view(); !got.Consulted || got.Action != governor.ActionRun || !got.At.Equal(govNow) {
				t.Errorf("call %d: %+v, want a consulted run as of now", i+1, got)
			}
		}
		if r.calls != 1 {
			t.Errorf("probe calls = %d, want 1", r.calls)
		}
		if _, err := os.Stat(r.usagePath()); err == nil {
			t.Error("usage.json was written for a provider with no reading")
		}
	})

	t.Run("a failing probe is tried once a minute and logged once", func(t *testing.T) {
		r := newGovRig(t)
		now := govNow
		d := r.usageDeps(provider.UsageSnapshot{}, errors.New("the tool did not answer"))
		d.Now = func() time.Time { return now }
		s := &usageSource{d: d}
		for _, step := range []struct {
			after     time.Duration
			wantCalls int
		}{{0, 1}, {5 * time.Second, 1}, {59 * time.Second, 1}, {60 * time.Second, 2}, {65 * time.Second, 2}} {
			now = govNow.Add(step.after)
			if got := s.view(); got.Action != governor.ActionHold {
				t.Errorf("at +%s: action %q, want hold with no reading", step.after, got.Action)
			}
			if r.calls != step.wantCalls {
				t.Errorf("at +%s: probe calls = %d, want %d", step.after, r.calls, step.wantCalls)
			}
		}
		if len(r.logs) != 1 {
			t.Errorf("%d log lines for one lasting problem, want 1", len(r.logs))
		}
	})

	t.Run("a reading is probed once and then reused, with no rewrite", func(t *testing.T) {
		r := newGovRig(t)
		now := govNow
		d := r.usageDeps(provider.UsageSnapshot{At: govNow, Windows: govWindows(30, 40, 50)}, nil)
		d.Now = func() time.Time { return now }
		s := &usageSource{d: d}
		s.view()
		first, err := os.ReadFile(r.usagePath())
		if err != nil {
			t.Fatal(err)
		}
		for _, after := range []time.Duration{5 * time.Second, time.Minute, 4 * time.Minute} {
			now = govNow.Add(after)
			s.view()
		}
		if again, _ := os.ReadFile(r.usagePath()); r.calls != 1 || !bytes.Equal(first, again) {
			t.Errorf("probe calls = %d, file changed = %v; want one probe and no rewrite inside the interval", r.calls, !bytes.Equal(first, again))
		}
		now = govNow.Add(5 * time.Minute)
		s.view()
		if r.calls != 2 {
			t.Errorf("probe calls = %d at the interval, want 2", r.calls)
		}
	})
}

// TestGovernorLoginSource covers the login check's schedule and what each
// answer leaves in governor.json.
//
// Load-bearing rows:
//   - "within the hour" and "logged out, checked a moment ago": the check
//     starts the agent's command-line tool, so a pass every few seconds must
//     not run it.
//   - "logged out is checked again at the probe interval": the owner logs in
//     and the build picks up within minutes, not within the hour.
//   - "unsupported" is unknown and still stamped, so it is not asked on every
//     pass; "unsupported forgets an old verdict" because a verdict kept from
//     another provider would hold launches for ever.
//   - both "error" rows: a check that could not run keeps the verdict it had.
//     Turning it into "logged out" would raise a false alarm.
func TestGovernorLoginSource(t *testing.T) {
	boom := errors.New("the tool did not answer")
	tests := []struct {
		name     string
		rec      *daemonstate.GovernorRecord
		status   provider.LoginStatus
		checkErr error

		wantCalls            int
		wantKnown, wantLogin bool
		// wantStamped: login_checked_at is this call's time afterwards.
		wantStamped bool
		wantLog     bool
	}{
		{name: "first check, logged in", status: provider.LoginStatus{LoggedIn: true},
			wantCalls: 1, wantKnown: true, wantLogin: true, wantStamped: true},
		{name: "first check, logged out", status: provider.LoginStatus{LoggedIn: false},
			wantCalls: 1, wantKnown: true, wantLogin: false, wantStamped: true},
		{name: "within the hour",
			rec:       &daemonstate.GovernorRecord{LoginCheckedAt: govStamp(59 * time.Minute), LoginKnown: true, LoggedIn: true},
			status:    provider.LoginStatus{LoggedIn: false},
			wantKnown: true, wantLogin: true},
		{name: "hourly",
			rec:       &daemonstate.GovernorRecord{LoginCheckedAt: govStamp(time.Hour), LoginKnown: true, LoggedIn: true},
			status:    provider.LoginStatus{LoggedIn: false},
			wantCalls: 1, wantKnown: true, wantLogin: false, wantStamped: true},
		{name: "logged out, checked a moment ago",
			rec:       &daemonstate.GovernorRecord{LoginCheckedAt: govStamp(4 * time.Minute), LoginKnown: true, LoggedIn: false},
			status:    provider.LoginStatus{LoggedIn: true},
			wantKnown: true, wantLogin: false},
		{name: "logged out is checked again at the probe interval",
			rec:       &daemonstate.GovernorRecord{LoginCheckedAt: govStamp(5 * time.Minute), LoginKnown: true, LoggedIn: false},
			status:    provider.LoginStatus{LoggedIn: true},
			wantCalls: 1, wantKnown: true, wantLogin: true, wantStamped: true},
		{name: "logged in is not checked at the probe interval",
			rec:       &daemonstate.GovernorRecord{LoginCheckedAt: govStamp(5 * time.Minute), LoginKnown: true, LoggedIn: true},
			status:    provider.LoginStatus{LoggedIn: false},
			wantKnown: true, wantLogin: true},
		{name: "unknown is not checked at the probe interval",
			rec:    &daemonstate.GovernorRecord{LoginCheckedAt: govStamp(5 * time.Minute)},
			status: provider.LoginStatus{LoggedIn: true}},
		{name: "unsupported", checkErr: provider.ErrLoginUnsupported,
			wantCalls: 1, wantStamped: true},
		{name: "unsupported forgets an old verdict",
			rec:       &daemonstate.GovernorRecord{LoginCheckedAt: govStamp(2 * time.Hour), LoginKnown: true, LoggedIn: false},
			checkErr:  provider.ErrLoginUnsupported,
			wantCalls: 1, wantStamped: true},
		{name: "error keeps a logged-out verdict",
			rec:       &daemonstate.GovernorRecord{LoginCheckedAt: govStamp(10 * time.Minute), LoginKnown: true, LoggedIn: false},
			checkErr:  boom,
			wantCalls: 1, wantKnown: true, wantLogin: false, wantStamped: true, wantLog: true},
		{name: "error keeps a logged-in verdict",
			rec:       &daemonstate.GovernorRecord{LoginCheckedAt: govStamp(2 * time.Hour), LoginKnown: true, LoggedIn: true},
			checkErr:  boom,
			wantCalls: 1, wantKnown: true, wantLogin: true, wantStamped: true, wantLog: true},
		{name: "error on the first check is unknown, not logged out",
			checkErr:  boom,
			wantCalls: 1, wantStamped: true, wantLog: true},
		{name: "a check stamped in the future counts as never checked",
			rec:       &daemonstate.GovernorRecord{LoginCheckedAt: govNow.Add(3 * time.Hour).Format(time.RFC3339), LoginKnown: true, LoggedIn: true},
			status:    provider.LoginStatus{LoggedIn: false},
			wantCalls: 1, wantKnown: true, wantLogin: false, wantStamped: true},
		{name: "a check time that cannot be read counts as never checked",
			rec:       &daemonstate.GovernorRecord{LoginCheckedAt: "yesterday", LoginKnown: true, LoggedIn: true},
			status:    provider.LoginStatus{LoggedIn: true},
			wantCalls: 1, wantKnown: true, wantLogin: true, wantStamped: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newGovRig(t)
			const tried = "2026-10-12T11:58:00Z"
			if tt.rec != nil {
				rec := *tt.rec
				rec.ProbeTriedAt = tried
				if err := daemonstate.WriteGovernor(r.dir(), rec); err != nil {
					t.Fatal(err)
				}
			}
			s := &loginSource{d: loginSourceDeps{
				Now:           func() time.Time { return govNow },
				Dir:           func() (string, error) { return usagestate.Dir(r.st) },
				ReadGovernor:  daemonstate.ReadGovernor,
				WriteGovernor: daemonstate.WriteGovernor,
				Check: func() (provider.LoginStatus, error) {
					r.calls++
					return tt.status, tt.checkErr
				},
				ProbeInterval: 5 * time.Minute,
				Log:           r.log,
			}}
			got := s.view()
			if r.calls != tt.wantCalls {
				t.Errorf("checks = %d, want %d", r.calls, tt.wantCalls)
			}
			if got.Known != tt.wantKnown || got.LoggedIn != tt.wantLogin {
				t.Errorf("view = %+v, want known %v logged in %v", got, tt.wantKnown, tt.wantLogin)
			}
			rec, ok := daemonstate.ReadGovernor(r.dir())
			if stamped := rec.LoginCheckedAt == govNow.Format(time.RFC3339); stamped != tt.wantStamped {
				t.Errorf("login_checked_at = %q, stamped now = %v, want %v", rec.LoginCheckedAt, stamped, tt.wantStamped)
			}
			if ok && (rec.LoginKnown != got.Known || rec.LoggedIn != got.LoggedIn) {
				t.Errorf("governor.json says %+v, the view says %+v", rec, got)
			}
			if tt.rec != nil && rec.ProbeTriedAt != tried {
				t.Errorf("probe_tried_at = %q, want it kept as %q", rec.ProbeTriedAt, tried)
			}
			if logged := len(r.logs) > 0; logged != tt.wantLog {
				t.Errorf("logged = %v (%v), want %v", logged, r.logs, tt.wantLog)
			}
		})
	}

	// The verdict still comes back when it cannot be saved or the folder
	// cannot be found; neither may turn into "logged out".
	t.Run("a verdict that cannot be saved is still returned", func(t *testing.T) {
		r := newGovRig(t)
		s := &loginSource{d: loginSourceDeps{
			Now:           func() time.Time { return govNow },
			Dir:           func() (string, error) { return r.dir(), nil },
			ReadGovernor:  daemonstate.ReadGovernor,
			WriteGovernor: func(string, daemonstate.GovernorRecord) error { return errors.New("disk full") },
			Check:         func() (provider.LoginStatus, error) { r.calls++; return provider.LoginStatus{LoggedIn: true}, nil },
			ProbeInterval: 5 * time.Minute,
			Log:           r.log,
		}}
		for i := 0; i < 2; i++ {
			if got := s.view(); !got.Known || !got.LoggedIn {
				t.Errorf("call %d: %+v, want known and logged in", i+1, got)
			}
		}
		if r.calls != 2 || len(r.logs) != 1 {
			t.Errorf("checks = %d, log lines = %d; want 2 checks and the problem logged once", r.calls, len(r.logs))
		}
	})
	t.Run("no state folder is unknown", func(t *testing.T) {
		r := newGovRig(t)
		s := &loginSource{d: loginSourceDeps{
			Now:   func() time.Time { return govNow },
			Dir:   func() (string, error) { return "", errors.New("no home") },
			Check: func() (provider.LoginStatus, error) { r.calls++; return provider.LoginStatus{}, nil },
			Log:   r.log,
		}}
		if got := s.view(); got.Known || r.calls != 0 {
			t.Errorf("view = %+v after %d checks, want unknown and no check", got, r.calls)
		}
	})
}

// stopRig records what a stop did. The process is "alive" until it has been
// sent dieAfter signals; zero means it never goes.
type stopRig struct {
	exitCalls []time.Duration
	exitErr   error
	meta      host.Meta
	metaErr   error
	cmdline   []string
	cmdErr    error
	signals   []syscall.Signal
	sigErr    error
	dead      bool
	dieAfter  int
	slept     time.Duration
}

func (r *stopRig) deps() stopDeps {
	return stopDeps{
		RequestExit: func(_, _ string, wait time.Duration) error {
			r.exitCalls = append(r.exitCalls, wait)
			return r.exitErr
		},
		ReadMeta: func(string, string) (host.Meta, error) { return r.meta, r.metaErr },
		Cmdline:  func(int) ([]string, error) { return r.cmdline, r.cmdErr },
		Signal: func(_ int, sig syscall.Signal) error {
			r.signals = append(r.signals, sig)
			if r.dieAfter > 0 && len(r.signals) >= r.dieAfter {
				r.dead = true
			}
			return r.sigErr
		},
		Alive: func(int) bool { return !r.dead },
		Sleep: func(d time.Duration) { r.slept += d },
	}
}

// TestGovernorStopSession covers the two ways a session is stopped for a
// pause. No signal is sent and no process is looked at.
//
// Load-bearing rows:
//   - "forced: the pid is another process now" and "forced: the command line
//     cannot be read": nothing is signalled. host.json outlives a crashed
//     host, and its pid may by now be any process of this user.
//   - "forced: the host will not go": an error, after the whole wait, with
//     SIGTERM and nothing else. A host killed outright leaves its agent
//     running where afk cannot reach it.
//   - "polite": no signal and no look at the pid at all.
func TestGovernorStopSession(t *testing.T) {
	hostArgs := []string{"/home/me/bin/afk", "_host", "--name", "t1-1", "--worktree", "/w/t1-1"}
	tests := []struct {
		name  string
		force bool
		rig   stopRig

		wantErr     []string
		wantExit    int
		wantSignals []syscall.Signal
		wantSlept   time.Duration
	}{
		{name: "polite", rig: stopRig{meta: host.Meta{PID: 77}, cmdline: hostArgs}, wantExit: 1},
		{name: "polite: the host cannot be reached", rig: stopRig{exitErr: errors.New("could not reach session t1-1")},
			wantExit: 1, wantErr: []string{"could not reach session t1-1"}},
		{name: "forced: the host goes", force: true,
			rig:         stopRig{meta: host.Meta{PID: 77}, cmdline: hostArgs, dieAfter: 1},
			wantSignals: []syscall.Signal{syscall.SIGTERM}},
		{name: "forced: the host will not go", force: true,
			rig:         stopRig{meta: host.Meta{PID: 77}, cmdline: hostArgs},
			wantSignals: []syscall.Signal{syscall.SIGTERM}, wantSlept: stopForceWait,
			wantErr: []string{"t1-1", "pid 77", "still running"}},
		{name: "forced: the host is already gone", force: true,
			rig: stopRig{meta: host.Meta{PID: 77}, cmdline: hostArgs, dead: true}},
		{name: "forced: the pid is another process now", force: true,
			rig:     stopRig{meta: host.Meta{PID: 77}, cmdline: []string{"/usr/bin/vim", "notes.txt"}},
			wantErr: []string{"t1-1", "was not signalled", "77", "not its host"}},
		{name: "forced: the pid is another session's host", force: true,
			rig:     stopRig{meta: host.Meta{PID: 77}, cmdline: []string{"/home/me/bin/afk", "_host", "--name", "t1-12", "--worktree", "/w/t1-12"}},
			wantErr: []string{"was not signalled"}},
		{name: "forced: the command line cannot be read", force: true,
			rig:     stopRig{meta: host.Meta{PID: 77}, cmdErr: errors.New("open /proc/77/cmdline: no such file or directory")},
			wantErr: []string{"was not signalled", "could not be confirmed", "/proc/77/cmdline"}},
		{name: "forced: no host record", force: true,
			rig:     stopRig{metaErr: errors.New("reading host.json: no such file")},
			wantErr: []string{"t1-1", "host record cannot be read"}},
		{name: "forced: a host record with no pid", force: true,
			rig:     stopRig{meta: host.Meta{}, cmdline: hostArgs},
			wantErr: []string{"no process id"}},
		{name: "forced: the signal fails and the host is still there", force: true,
			rig:         stopRig{meta: host.Meta{PID: 77}, cmdline: hostArgs, sigErr: syscall.EPERM},
			wantSignals: []syscall.Signal{syscall.SIGTERM},
			wantErr:     []string{"stopping its host", "operation not permitted"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := tt.rig
			err := daemonStopSession(r.deps())("/w/t1-1", "t1-1", tt.force)
			if (err != nil) != (len(tt.wantErr) > 0) {
				t.Fatalf("err = %v, want an error: %v", err, len(tt.wantErr) > 0)
			}
			for _, w := range tt.wantErr {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error lacks %q: %v", w, err)
				}
			}
			if len(r.exitCalls) != tt.wantExit {
				t.Errorf("exit requests = %d, want %d", len(r.exitCalls), tt.wantExit)
			}
			for _, w := range r.exitCalls {
				if w != 2*time.Second {
					t.Errorf("exit request waits %s, want 2s", w)
				}
			}
			if len(r.signals) != len(tt.wantSignals) {
				t.Fatalf("signals = %v, want %v", r.signals, tt.wantSignals)
			}
			for i, s := range r.signals {
				if s != tt.wantSignals[i] {
					t.Errorf("signal %d = %v, want %v", i, s, tt.wantSignals[i])
				}
				if s == syscall.SIGKILL {
					t.Error("the host was sent SIGKILL")
				}
			}
			if r.slept != tt.wantSlept {
				t.Errorf("waited %s, want %s", r.slept, tt.wantSlept)
			}
		})
	}
}

// TestGovernorIsSessionHost pins what counts as "this session's host" on a
// command line. Load-bearing: a session whose name only starts with this
// one's, and a process that merely mentions the name without being a host.
func TestGovernorIsSessionHost(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{"the host", []string{"/home/me/bin/afk", "_host", "--name", "t1-1", "--worktree", "/w/t1-1"}, true},
		{"the host under another program name", []string{"/tmp/afk-check", "_host", "--name", "t1-1"}, true},
		{"name written with an equals sign", []string{"afk", "_host", "--name=t1-1"}, true},
		{"another session's host", []string{"afk", "_host", "--name", "t1-12", "--worktree", "/w/t1-12"}, false},
		{"the name only inside a path", []string{"afk", "_host", "--name", "other", "--worktree", "/w/t1-1"}, false},
		{"not a host", []string{"afk", "attach", "t1-1"}, false},
		{"the name before the host word", []string{"t1-1", "_host", "--name", "other"}, false},
		{"another program", []string{"/usr/bin/vim", "t1-1"}, false},
		{"nothing", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSessionHost(tt.args, "t1-1"); got != tt.want {
				t.Errorf("isSessionHost(%q) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
	if isSessionHost([]string{"afk", "_host", "--name", ""}, "") {
		t.Error("an empty session name matched")
	}
}

// TestGovernorProcCmdline reads this test process's own command line, the
// only real process it may look at. It is skipped where there is no /proc,
// which is also where a forced stop refuses to signal.
func TestGovernorProcCmdline(t *testing.T) {
	if _, err := os.Stat("/proc/self/cmdline"); err != nil {
		t.Skip("no /proc on this system")
	}
	got, err := procCmdline(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(os.Args) {
		t.Fatalf("got %d arguments %q, want the %d of os.Args %q", len(got), got, len(os.Args), os.Args)
	}
	for i := range got {
		if got[i] != os.Args[i] {
			t.Errorf("argument %d = %q, want %q", i, got[i], os.Args[i])
		}
	}
	if _, err := procCmdline(-1); err == nil {
		t.Error("no error for a process that cannot exist")
	}
}

// TestGovernorStatusLine pins the machine-wide usage line of `afk daemon
// status`. Load-bearing: "nothing stored" and "an empty action" print
// nothing, and the pause rows show the wake time in the reader's zone.
func TestGovernorStatusLine(t *testing.T) {
	at := time.Date(2026, 10, 12, 12, 30, 0, 0, time.UTC)
	wake := time.Date(2026, 10, 12, 17, 1, 0, 0, time.UTC)
	tests := []struct {
		name string
		g    *usagestate.Governor
		loc  *time.Location
		want string
	}{
		{name: "nothing stored", g: nil, want: ""},
		{name: "an empty action", g: &usagestate.Governor{At: at}, want: ""},
		{name: "pause with a wake time",
			g:    &usagestate.Governor{Action: "pause", Reason: "The current session is 91% used.", WakeAt: &wake, At: at},
			want: "Usage: paused until Mon 12 Oct 17:01. The current session is 91% used. (as of Mon 12 Oct 12:30)"},
		{name: "pause in the reader's zone",
			g:    &usagestate.Governor{Action: "pause", Reason: "The current session is 91% used.", WakeAt: &wake, At: at},
			loc:  time.FixedZone("west", -5*3600),
			want: "Usage: paused until Mon 12 Oct 12:01. The current session is 91% used. (as of Mon 12 Oct 07:30)"},
		{name: "pause with no wake time",
			g:    &usagestate.Governor{Action: "pause", Reason: "The current session is 91% used.", At: at},
			want: "Usage: paused, with no known time to start again. The current session is 91% used. (as of Mon 12 Oct 12:30)"},
		{name: "run",
			g:    &usagestate.Governor{Action: "run", Reason: "Usage is within the limits, so afk is running.", At: at},
			want: "Usage: within the limits. Usage is within the limits, so afk is running. (as of Mon 12 Oct 12:30)"},
		{name: "no launch",
			g:    &usagestate.Governor{Action: "no_launch", Reason: "The current session is 80% used.", At: at},
			want: "Usage: nothing new is started. The current session is 80% used. (as of Mon 12 Oct 12:30)"},
		{name: "hold",
			g:    &usagestate.Governor{Action: "hold", Reason: "The last usage reading is 31 minutes old.", At: at},
			want: "Usage: not known, so nothing new is started. The last usage reading is 31 minutes old. (as of Mon 12 Oct 12:30)"},
		{name: "refuse",
			g:    &usagestate.Governor{Action: "refuse", Reason: "Extra usage is switched on.", At: at},
			want: "Usage: afk will not run. Extra usage is switched on. (as of Mon 12 Oct 12:30)"},
		{name: "a word from a newer afk is shown as it is",
			g:    &usagestate.Governor{Action: "throttle", At: at},
			want: "Usage: throttle. (as of Mon 12 Oct 12:30)"},
		{name: "no time",
			g:    &usagestate.Governor{Action: "run", Reason: "Fine."},
			want: "Usage: within the limits. Fine."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := usageStatusLine(tt.g, tt.loc); got != tt.want {
				t.Errorf("got  %q\nwant %q", got, tt.want)
			}
		})
	}
}
