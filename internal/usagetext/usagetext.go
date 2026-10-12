// Package usagetext holds the words afk uses for the usage governor's stored
// decision and for the usage windows, so the commands and the dashboard say
// the same thing and cannot drift apart. It is pure: no filesystem, no clock
// of its own (callers pass "now"), and it never re-runs the governor, because
// the thresholds may have changed since the decision was stored and the text
// is about what the daemon is acting on.
package usagetext

import (
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/dphaener/afk/internal/governor"
	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/usagestate"
)

// layout is the wake-time format shared with the commands.
const layout = "Mon 2 Jan 15:04"

// StaleAfter matches the governor's default stale_after: a reading older than
// this is marked, because usage.json outlives the daemon and an old reading
// must not read as the present.
const StaleAfter = 30 * time.Minute

// Lead is the plain-words part of the governor line, or "" for a nil governor
// or an empty action. An action it does not know is shown as stored, never
// guessed at.
func Lead(g *usagestate.Governor, loc *time.Location) string {
	if g == nil || g.Action == "" {
		return ""
	}
	if loc == nil {
		loc = time.UTC
	}
	switch governor.Action(g.Action) {
	case governor.ActionRun:
		return "within the limits"
	case governor.ActionNoLaunch:
		return "nothing new is started"
	case governor.ActionPause:
		if g.WakeAt != nil {
			return "paused until " + g.WakeAt.In(loc).Format(layout)
		}
		return "paused, with no known time to start again"
	case governor.ActionHold:
		return "not known, so nothing new is started"
	case governor.ActionRefuse:
		return "afk will not run"
	}
	return g.Action
}

// WindowLabel names a usage window. The scope arrives as data (a model name
// for the per-model weekly window).
func WindowLabel(kind, scope string) string {
	switch kind {
	case provider.WindowSession:
		return "Session"
	case provider.WindowWeekly:
		return "Week (all models)"
	case provider.WindowWeeklyModel:
		return "Week (" + scope + ")"
	}
	return "Other (" + scope + ")"
}

// Tightest returns the window with the highest UsedPct. On a tie it takes the
// one that resets later, since that one holds the build back longest; a window
// with no reset time counts as the latest. False for an empty list.
func Tightest(ws []usagestate.Window) (usagestate.Window, bool) {
	if len(ws) == 0 {
		return usagestate.Window{}, false
	}
	best := ws[0]
	for _, w := range ws[1:] {
		switch {
		case w.UsedPct > best.UsedPct:
			best = w
		case w.UsedPct == best.UsedPct && laterReset(w, best):
			best = w
		}
	}
	return best, true
}

// laterReset reports whether a resets strictly later than b, with an unknown
// reset time counting as later than any known one.
func laterReset(a, b usagestate.Window) bool {
	if a.ResetsAt == nil {
		return b.ResetsAt != nil
	}
	if b.ResetsAt == nil {
		return false
	}
	return a.ResetsAt.After(*b.ResetsAt)
}

// shortLead is one word per action; an unknown action is shown as stored.
func shortLead(g *usagestate.Governor) string {
	if g == nil || g.Action == "" {
		return ""
	}
	switch governor.Action(g.Action) {
	case governor.ActionRun:
		return "ok"
	case governor.ActionNoLaunch:
		return "no new tasks"
	case governor.ActionPause:
		return "paused"
	case governor.ActionHold:
		return "unknown"
	case governor.ActionRefuse:
		return "refused"
	}
	return g.Action
}

// Segment is the dashboard header text, unstyled: a long form and a short
// one for narrow widths. known false (no reading at all) gives two empty
// strings, as do a reading with neither a decision nor a window. A reading
// older than StaleAfter is marked in both forms.
func Segment(f usagestate.File, known bool, now time.Time, loc *time.Location) (long, short string) {
	if !known {
		return "", ""
	}
	lead := Lead(f.Governor, loc)
	sLead := shortLead(f.Governor)
	w, hasWin := Tightest(f.Windows)
	if lead == "" && !hasWin {
		return "", ""
	}
	pct := ""
	if hasWin {
		pct = strconv.Itoa(int(math.Round(w.UsedPct))) + "%"
	}
	switch {
	case lead != "" && hasWin:
		long = "usage: " + lead + " · " + WindowLabel(w.Kind, w.Scope) + " " + pct
		short = sLead + " · " + pct
	case lead != "":
		long = "usage: " + lead
		short = sLead
	default:
		long = "usage: " + WindowLabel(w.Kind, w.Scope) + " " + pct
		short = pct
	}
	if !f.At.IsZero() && now.Sub(f.At) > StaleAfter {
		long += " (read " + ageText(now.Sub(f.At)) + " ago)"
		short += " (old)"
	}
	return long, short
}

// ageText is a coarse age: minutes under two hours, hours under two days,
// then days.
func ageText(d time.Duration) string {
	switch {
	case d < 2*time.Hour:
		return fmt.Sprintf("%d minutes", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours", int(d/time.Hour))
	}
	return fmt.Sprintf("%d days", int(d/(24*time.Hour)))
}
