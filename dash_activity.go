package main

import (
	"time"

	"github.com/dphaener/afk/internal/activity"
	"github.com/dphaener/afk/internal/decisionlog"
	"github.com/dphaener/afk/internal/tokens"
	"github.com/dphaener/afk/internal/ui"
)

// dashActivityParts are the lookups behind the home screen's activity
// column and week chart, injected so the loader can be tested against temp
// folders and a fixed zone.
type dashActivityParts struct {
	// tail returns the last max bytes of a log as whole lines
	// (activity.Tail); a missing file is no lines and no error.
	tail func(path string, max int64) ([]string, error)
	// loc is the zone the week's days are cut in.
	loc *time.Location
}

func realDashActivityParts() dashActivityParts {
	return dashActivityParts{tail: activity.Tail, loc: time.Local}
}

// dashLoadActivity returns ui.Deps.LoadActivity for the real dashboard.
func dashLoadActivity() func(roots []string, now time.Time) ui.HomeActivity {
	p := realDashActivityParts()
	return func(roots []string, now time.Time) ui.HomeActivity {
		return loadDashActivity(p, roots, now)
	}
}

// loadDashActivity reads the end of each root's decision log and token
// ledger. It only reads. A log that cannot be read costs that root its
// cells (decision log) or its share of the week (token ledger) and never
// stops the other roots: a half-drawn column is more useful than none, and
// the cell stays blank rather than claiming a quiet day it did not measure.
// WeekKnown is true once any ledger was read, even an empty or missing one,
// because a project with no ledger yet really did use nothing this week.
func loadDashActivity(p dashActivityParts, roots []string, now time.Time) ui.HomeActivity {
	out := ui.HomeActivity{Lines: map[string][activity.Buckets]int{}}
	for _, root := range roots {
		if lines, err := p.tail(decisionlog.Path(root), activity.MaxRead); err == nil {
			out.Lines[root] = activity.Count(lines, now)
		}
		lines, err := p.tail(tokens.LedgerPath(root), activity.MaxRead)
		if err != nil {
			continue
		}
		wk := activity.WeekSums(lines, now, p.loc)
		if out.WeekKnown {
			out.Week.Add(wk)
		} else {
			// The first week read sets the days; Add keeps them.
			out.Week = wk
			out.WeekKnown = true
		}
	}
	return out
}
