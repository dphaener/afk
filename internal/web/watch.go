package web

import (
	"sync"
	"time"
)

// The watch view reads an agent's screen from Rex. A busy block is about
// 0.9 MB of HTML and 200-350 ms to render (T17.1), so every number below
// exists to keep that cost off the daemon's 2-second tick unless someone is
// actually looking.
const (
	// watchLease is how long a watcher counts after its last ping. The page
	// pings every 4 seconds, so 10 seconds survives two missed pings (a
	// phone waking up) yet stops reading soon after the tab closes.
	watchLease = 10 * time.Second

	// watchEvery is the poll while the block is under the bound. It matches
	// the hub's 2-second tick, the fastest anything on this page refreshes.
	watchEvery = 2 * time.Second

	// watchSlowEvery is the poll above the bound and after a failed read.
	// A gone or oversized block is retried rarely so it cannot pin Rex.
	watchSlowEvery = 10 * time.Second

	// watchBound is the size in bytes of HTML above which the watch polls
	// slowly. It keeps a 0.9 MB scrollback off the 2-second tick.
	watchBound = 256 << 10
)

// WatchInputs is everything watchReadDue decides from. The caller gathers
// it, so the decision is table-testable with no clock and no Rex.
type WatchInputs struct {
	// Watchers is the number of live leases for this session.
	Watchers int
	// LastRead is when the screen was last read; zero if never.
	LastRead time.Time
	// Now is the current time, passed in so the decision never reads the clock.
	Now time.Time
	// LastTotal is the block size from the last probe; -1 when unknown.
	LastTotal int64
	// LastFailed is true when the last read failed.
	LastFailed bool
}

// watchReadDue says whether the screen should be read now, and whether the
// slow cadence applies. With no watcher it is never due: the reader must
// never read for nobody, because each read costs Rex a full render.
func watchReadDue(in WatchInputs) (due bool, slow bool) {
	if in.Watchers <= 0 {
		return false, false
	}
	if in.LastRead.IsZero() {
		return true, false
	}
	since := in.Now.Sub(in.LastRead)
	if in.LastFailed {
		return since >= watchSlowEvery, true
	}
	if in.LastTotal > watchBound {
		return since >= watchSlowEvery, true
	}
	return since >= watchEvery, false
}

// watchLeaseKey is one watched session: a project and a session name.
type watchLeaseKey = string

// watchLeases counts live watchers per session. A watcher is a browser page
// that pinged recently; counting by lease means a closed tab stops costing
// reads within watchLease without any explicit goodbye.
type watchLeases struct {
	mu     sync.Mutex
	leases map[watchLeaseKey]map[string]time.Time
}

// Touch records that watcher is watching key as of now.
func (l *watchLeases) Touch(key, watcher string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.leases == nil {
		l.leases = map[watchLeaseKey]map[string]time.Time{}
	}
	if l.leases[key] == nil {
		l.leases[key] = map[string]time.Time{}
	}
	l.leases[key][watcher] = now
}

// Count returns the live watchers of key. A lease is live while now is
// before its last ping plus watchLease.
func (l *watchLeases) Count(key string, now time.Time) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, last := range l.leases[key] {
		if now.Before(last.Add(watchLease)) {
			n++
		}
	}
	return n
}

// Prune drops lapsed leases so the registry does not grow with every
// browser that ever watched.
func (l *watchLeases) Prune(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for key, ws := range l.leases {
		for w, last := range ws {
			if !now.Before(last.Add(watchLease)) {
				delete(ws, w)
			}
		}
		if len(ws) == 0 {
			delete(l.leases, key)
		}
	}
}
