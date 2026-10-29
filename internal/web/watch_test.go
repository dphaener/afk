package web

import (
	"testing"
	"time"
)

// TestWatchReadDue: the no-watcher row is load-bearing (a read with nobody
// watching burns a full Rex render for nothing) and so is the bound-edge row
// (a total of exactly watchBound is still the fast cadence; only above it is
// slow).
func TestWatchReadDue(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	cases := []struct {
		name     string
		in       WatchInputs
		due, slw bool
	}{
		{"no watchers", WatchInputs{Watchers: 0, Now: now, LastTotal: -1}, false, false},
		{"no watchers never read", WatchInputs{Watchers: 0, Now: now, LastRead: ago(time.Hour), LastTotal: 10}, false, false},
		{"two watchers like one", WatchInputs{Watchers: 2, Now: now, LastRead: ago(2 * time.Second), LastTotal: 10}, true, false},
		{"never read", WatchInputs{Watchers: 1, Now: now, LastTotal: -1}, true, false},
		{"read 1.9s ago", WatchInputs{Watchers: 1, Now: now, LastRead: ago(1900 * time.Millisecond), LastTotal: 10}, false, false},
		{"read 2s ago", WatchInputs{Watchers: 1, Now: now, LastRead: ago(2 * time.Second), LastTotal: 10}, true, false},
		{"failed 5s ago", WatchInputs{Watchers: 1, Now: now, LastRead: ago(5 * time.Second), LastFailed: true, LastTotal: -1}, false, true},
		{"failed 10s ago", WatchInputs{Watchers: 1, Now: now, LastRead: ago(10 * time.Second), LastFailed: true, LastTotal: -1}, true, true},
		{"above bound 5s ago", WatchInputs{Watchers: 1, Now: now, LastRead: ago(5 * time.Second), LastTotal: watchBound + 1}, false, true},
		{"above bound 10s ago", WatchInputs{Watchers: 1, Now: now, LastRead: ago(10 * time.Second), LastTotal: watchBound + 1}, true, true},
		{"exactly the bound is fast", WatchInputs{Watchers: 1, Now: now, LastRead: ago(2 * time.Second), LastTotal: watchBound}, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			due, slow := watchReadDue(c.in)
			if due != c.due || slow != c.slw {
				t.Errorf("watchReadDue = (%v, %v), want (%v, %v)", due, slow, c.due, c.slw)
			}
		})
	}
}

// TestWatchLeases: the lapse row is load-bearing (a closed tab must stop
// costing reads) and the same-watcher row (a page pinging every 4 s must not
// look like many watchers).
func TestWatchLeases(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	const key = "proj/sess"
	cases := []struct {
		name string
		run  func(l *watchLeases) int
		want int
	}{
		{"no lease counts zero", func(l *watchLeases) int { return l.Count(key, t0) }, 0},
		{"one ping counts one", func(l *watchLeases) int {
			l.Touch(key, "a", t0)
			return l.Count(key, t0.Add(time.Second))
		}, 1},
		{"a ping renews", func(l *watchLeases) int {
			l.Touch(key, "a", t0)
			l.Touch(key, "a", t0.Add(8*time.Second))
			return l.Count(key, t0.Add(15*time.Second))
		}, 1},
		{"lapses after 10s", func(l *watchLeases) int {
			l.Touch(key, "a", t0)
			return l.Count(key, t0.Add(watchLease))
		}, 0},
		{"live just before 10s", func(l *watchLeases) int {
			l.Touch(key, "a", t0)
			return l.Count(key, t0.Add(watchLease-time.Nanosecond))
		}, 1},
		{"two watchers", func(l *watchLeases) int {
			l.Touch(key, "a", t0)
			l.Touch(key, "b", t0)
			return l.Count(key, t0)
		}, 2},
		{"same watcher twice", func(l *watchLeases) int {
			l.Touch(key, "a", t0)
			l.Touch(key, "a", t0.Add(time.Second))
			return l.Count(key, t0.Add(2*time.Second))
		}, 1},
		{"other session not counted", func(l *watchLeases) int {
			l.Touch("proj/other", "a", t0)
			return l.Count(key, t0)
		}, 0},
		{"prune drops lapsed keeps live", func(l *watchLeases) int {
			l.Touch(key, "a", t0)
			l.Touch(key, "b", t0.Add(8*time.Second))
			l.Prune(t0.Add(11 * time.Second))
			return len(l.leases[key])
		}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var l watchLeases
			if got := c.run(&l); got != c.want {
				t.Errorf("got %d, want %d", got, c.want)
			}
		})
	}
}
