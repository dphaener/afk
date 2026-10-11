// Package governor decides whether afk may launch, continue or must pause
// work given the latest usage snapshot. It implements the table in
// docs/PLAN.md §6.3 as one pure function.
//
// It is pure because PLAN §3 requires every decision that branches on the
// environment to be table-testable without a clock, a CLI or a filesystem,
// and because the daemon (T2.4) calls it on every probe tick: the caller
// supplies the snapshot, the session state and the time, and gets back a
// plain value. The package does no I/O and never calls time.Now.
//
// Unsupported is checked before the snapshot rows even though §6.3 lists it
// last: a provider that cannot report usage never has a snapshot, so the
// "no snapshot" row would otherwise always win and on_unknown_usage = run
// could never take effect.
package governor

import (
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/dphaener/afk/internal/provider"
)

// Action is what the caller should do.
type Action string

const (
	// ActionRun means launch and continue as normal.
	ActionRun Action = "run"
	// ActionNoLaunch means start nothing new; a running session continues.
	ActionNoLaunch Action = "no_launch"
	// ActionPause means pause the running session until WakeAt.
	ActionPause Action = "pause"
	// ActionHold means start nothing new and leave a running session alone,
	// because usage is unknown.
	ActionHold Action = "hold"
	// ActionRefuse means do not run at all until the owner acts.
	ActionRefuse Action = "refuse"
)

// Values for Thresholds.OnUnknownUsage.
const (
	UnknownRun  = "run"
	UnknownHold = "hold"
)

// DefaultProbeInterval is how often the daemon should refresh the snapshot.
const DefaultProbeInterval = 5 * time.Minute

// weeklyMargin is how far below PauseAt the weekly no-launch line sits, so a
// week that is nearly spent stops accepting new work before it must pause.
const weeklyMargin = 5

// Thresholds are the limits Decide applies, all in percent except the age.
type Thresholds struct {
	PauseAt        float64
	LaunchBelow    float64
	StaleAfter     time.Duration
	OnUnknownUsage string
}

// DefaultThresholds returns the PLAN §6.3 defaults.
func DefaultThresholds() Thresholds {
	return Thresholds{PauseAt: 90, LaunchBelow: 75, StaleAfter: 30 * time.Minute, OnUnknownUsage: UnknownRun}
}

// Input is everything Decide looks at.
type Input struct {
	Snapshot       *provider.UsageSnapshot
	Unsupported    bool
	SessionRunning bool
	Now            time.Time
	Thresholds     Thresholds
}

// Decision is the verdict and the facts behind it.
type Decision struct {
	Action Action
	// Reason is one plain sentence for the owner.
	Reason string
	// Window is the window that decided a pause or no-launch; nil otherwise.
	Window *provider.Window
	// WakeAt is when a paused session may resume; zero when unknown or not
	// a pause.
	WakeAt time.Time
	// PauseSession is true only for a pause while a session is running.
	PauseSession bool
}

// Decide applies the rules in order; the first match wins.
func Decide(in Input) Decision {
	th := in.Thresholds

	if in.Unsupported {
		if th.OnUnknownUsage == UnknownRun {
			return Decision{Action: ActionRun, Reason: "Usage cannot be read for this tool, so afk is running without a usage limit."}
		}
		return Decision{Action: ActionHold, Reason: "Usage cannot be read for this tool, so afk is holding new work."}
	}
	s := in.Snapshot
	if s == nil {
		return Decision{Action: ActionHold, Reason: "Usage has not been read yet, so afk is holding new work."}
	}
	if s.OverageEnabled {
		return Decision{Action: ActionRefuse, Reason: "Extra usage is switched on for this account, so afk will not run; turn extra usage off to continue."}
	}
	if in.Now.Sub(s.At) >= th.StaleAfter {
		return Decision{Action: ActionHold, Reason: fmt.Sprintf(
			"The last usage reading is %s old, which is past the %s limit, so afk is holding new work.",
			durText(in.Now.Sub(s.At)), durText(th.StaleAfter))}
	}

	var top *provider.Window
	var wake time.Time
	zeroReset := false
	for i := range s.Windows {
		w := &s.Windows[i]
		if w.UsedPct < th.PauseAt {
			continue
		}
		if top == nil || w.UsedPct > top.UsedPct {
			top = w
		}
		if w.ResetsAt.IsZero() {
			zeroReset = true
		} else if w.ResetsAt.After(wake) {
			wake = w.ResetsAt
		}
	}
	if top != nil {
		d := Decision{
			Action:       ActionPause,
			Window:       copyWindow(top),
			PauseSession: in.SessionRunning,
			Reason: fmt.Sprintf("%s is %s used, at or past the %s pause limit, so afk is pausing until it resets.",
				windowText(top), pct(top.UsedPct), pct(th.PauseAt)),
		}
		if !zeroReset && !wake.IsZero() {
			d.WakeAt = wake.Add(time.Minute)
		}
		return d
	}

	var hit *provider.Window
	var limit float64
	for i := range s.Windows {
		w := &s.Windows[i]
		var l float64
		switch w.Kind {
		case provider.WindowSession:
			l = th.LaunchBelow
		case provider.WindowWeekly, provider.WindowWeeklyModel:
			l = th.PauseAt - weeklyMargin
		default:
			continue
		}
		if w.UsedPct >= l && (hit == nil || w.UsedPct > hit.UsedPct) {
			hit, limit = w, l
		}
	}
	if hit != nil {
		return Decision{
			Action: ActionNoLaunch,
			Window: copyWindow(hit),
			Reason: fmt.Sprintf("%s is %s used, at or past the limit of %s for starting new work, so afk is not starting anything new.",
				windowText(hit), pct(hit.UsedPct), pct(limit)),
		}
	}
	return Decision{Action: ActionRun, Reason: "Usage is within the limits, so afk is running."}
}

func copyWindow(w *provider.Window) *provider.Window {
	c := *w
	return &c
}

// windowText names a window in words, using Scope for model and unknown rows.
func windowText(w *provider.Window) string {
	switch w.Kind {
	case provider.WindowSession:
		return "The current session"
	case provider.WindowWeekly:
		return "This week's usage"
	case provider.WindowWeeklyModel:
		if w.Scope != "" {
			return "This week's usage for " + w.Scope
		}
		return "This week's usage for one model"
	}
	if w.Scope != "" {
		return "The " + w.Scope + " usage window"
	}
	return "A usage window"
}

// pct renders a percentage rounded to one decimal so 89.9 never reads as 90.
func pct(v float64) string {
	return strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64) + "%"
}

func durText(d time.Duration) string {
	m := int(d / time.Minute)
	if m == 1 {
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", m)
}
