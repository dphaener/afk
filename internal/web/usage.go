package web

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dphaener/afk/internal/usagestate"
)

// This file is the Refresh usage button's server side: POST /usage/refresh
// starts one usage reading in the background, and the usage view says whether
// one is running and how the last one went.
//
// The reading runs on its own goroutine and never inside the request, for two
// reasons. It can take up to a minute (it runs the agent's command-line
// tool), longer than a phone's browser or a proxy will hold a request open,
// and longer than the server's own write timeout. And a reading the owner
// asked for should land even if they close the tab: it writes usage.json,
// which the page and the build loop then read like any other reading.

// Fallback sentences for a result that came back with no text, so the owner
// always hears something, as the dashboard does for its refresh. None names
// an agent.
const (
	refreshedFallback   = "Usage was read and saved."
	unsupportedFallback = "The agent cannot report its usage, so there is nothing to read."
	failedFallback      = "Usage could not be read. The last reading stays. Run afk usage in a terminal to see why."
	// refreshPanicText is the result when the reading itself panicked. The
	// panic is recovered so the daemon keeps running and the button works
	// again; there is no log to write it to (the package logs nothing).
	refreshPanicText = "The reading stopped unexpectedly. Try again."
	// unknownOutcomeText replaces the text of an outcome this package does
	// not know: the seam is from the same binary, so this is a fault, and it
	// is shown as a failure because claiming success could hide one.
	unknownOutcomeText = "The usage reading gave an answer this page does not know. This is a fault in afk; please report it. Run afk usage in a terminal to see the last reading."
)

// The states UsageRefreshView.State takes.
const (
	// refreshIdle: no reading has finished in this daemon run and none is
	// running. The page then has nothing to say about refreshing.
	refreshIdle    = "idle"
	refreshRunning = "running"
)

// refreshJustNow is how long after a successful reading the page may say it
// was checked just now.
const refreshJustNow = time.Minute

// UsageRefreshView is the usage view's refresh block: whether a reading is
// running and how the last one in this daemon run went. It is the
// refresher's memory, not usage.json: a failed reading writes no file, so
// this is the only place its sentence exists.
type UsageRefreshView struct {
	// State is "idle", "running", or the last finished reading's outcome
	// ("refreshed", "failed", "unsupported"). A running reading wins over
	// the last result.
	State string `json:"state"`
	// Text is the last finished reading's sentence, or "" when idle or
	// running.
	Text string `json:"text"`
	// At is when the running reading started, or when the last one
	// finished; left out when idle.
	At *time.Time `json:"at,omitempty"`
	// JustChecked is true when the last reading succeeded less than
	// refreshJustNow before the view was built, so the page can say
	// "Checked just now". Only the page reads it.
	JustChecked bool `json:"-"`
}

// usageRefresher runs at most one usage reading at a time and remembers how
// the last one went. It is owned by the server and lives as long as it.
//
// The one-at-a-time guard is here because nothing else holds it: the root's
// entry point is the same one the command and the dashboard call, and it
// guards nothing itself. Two readings at once would run the agent's tool
// twice and race to write usage.json.
type usageRefresher struct {
	now func() time.Time
	// wg counts the running reading's goroutine, so a test can wait for it.
	wg sync.WaitGroup

	mu      sync.Mutex
	running bool
	started time.Time
	// last is the last finished reading's result, valid when done is true.
	done   bool
	last   UsageRefreshResult
	lastAt time.Time
	// gen goes up when a reading starts and when it finishes. The live
	// stream folds it into the usage part's fingerprint, so each of those
	// sends one usage event: a failure writes no file, and without this an
	// open page would never learn of it.
	gen uint64
}

// refreshSnapshot is a copy of the refresher's state at one moment.
type refreshSnapshot struct {
	Running bool
	Started time.Time
	Done    bool
	Last    UsageRefreshResult
	LastAt  time.Time
	Gen     uint64
}

func newUsageRefresher(now func() time.Time) *usageRefresher {
	if now == nil {
		now = time.Now
	}
	return &usageRefresher{now: now}
}

// start runs fn on its own goroutine, unless a reading is already running, in
// which case it does nothing and returns false. fn takes no request and no
// context on purpose: the reading must outlive the request that asked for
// it (the owner may close the tab), and the probe's own timeout bounds it.
func (u *usageRefresher) start(fn func() UsageRefreshResult) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.running {
		return false
	}
	u.running = true
	u.started = u.now()
	u.gen++
	u.wg.Add(1)
	go func() {
		defer u.wg.Done()
		res := UsageRefreshResult{Outcome: UsageRefreshFailed, Text: refreshPanicText}
		defer func() {
			// A panic leaves res as set above. Recovering here keeps the
			// daemon (and its build loop) alive, and finish still clears
			// running so the button works again.
			_ = recover()
			u.finish(res)
		}()
		res = fn()
	}()
	return true
}

// finish stores a finished reading's result and clears running.
func (u *usageRefresher) finish(res UsageRefreshResult) {
	res = normalizeRefresh(res)
	u.mu.Lock()
	defer u.mu.Unlock()
	u.running = false
	u.done = true
	u.last = res
	u.lastAt = u.now()
	u.gen++
}

// normalizeRefresh makes a result fit to show: an unknown outcome becomes a
// failure, and an empty text gets its outcome's fallback sentence. Pure.
func normalizeRefresh(res UsageRefreshResult) UsageRefreshResult {
	res.Text = strings.TrimSpace(res.Text)
	switch res.Outcome {
	case UsageRefreshed:
		if res.Text == "" {
			res.Text = refreshedFallback
		}
	case UsageRefreshUnsupported:
		if res.Text == "" {
			res.Text = unsupportedFallback
		}
	case UsageRefreshFailed:
		if res.Text == "" {
			res.Text = failedFallback
		}
	default:
		res = UsageRefreshResult{Outcome: UsageRefreshFailed, Text: unknownOutcomeText}
	}
	return res
}

// snapshot copies the refresher's state for a view or the live reader.
func (u *usageRefresher) snapshot() refreshSnapshot {
	u.mu.Lock()
	defer u.mu.Unlock()
	return refreshSnapshot{Running: u.running, Started: u.started, Done: u.done, Last: u.last, LastAt: u.lastAt, Gen: u.gen}
}

// generation is the live reader's view of the refresher: a number that
// changes whenever a reading starts or finishes.
func (u *usageRefresher) generation() uint64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.gen
}

// wait blocks until no reading goroutine is running. Only tests call it, so
// none outlives the test that started it.
func (u *usageRefresher) wait() { u.wg.Wait() }

// refreshViewFor is the refresh block for a snapshot at now. Pure.
func refreshViewFor(sn refreshSnapshot, now time.Time) *UsageRefreshView {
	switch {
	case sn.Running:
		at := sn.Started
		return &UsageRefreshView{State: refreshRunning, At: &at}
	case sn.Done:
		at := sn.LastAt
		v := &UsageRefreshView{State: sn.Last.Outcome, Text: sn.Last.Text, At: &at}
		v.JustChecked = sn.Last.Outcome == UsageRefreshed && !now.Before(at) && now.Sub(at) < refreshJustNow
		return v
	}
	return &UsageRefreshView{State: refreshIdle}
}

// refreshUsage is POST /usage/refresh and /api/usage/refresh. It starts a
// reading and returns at once; the reading lands through the live stream's
// usage event. No confirm is asked for: a reading changes nothing the owner
// owns (it rewrites the stored reading with a newer one), and the dashboard's
// refresh has none either.
func (s *server) refreshUsage(w http.ResponseWriter, r *http.Request) {
	if s.d.RefreshUsage == nil {
		s.notFound(w, r)
		return
	}
	// The body carries nothing this handler needs. It is still read, so a
	// body over the limit gets the same answer as on every other POST; any
	// other fault in it is ignored, because nothing is taken from it.
	if _, err := readPostBody(r); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			s.tooLarge(w, r)
			return
		}
	}
	started := s.refresher.start(s.d.RefreshUsage)
	if isAPI(r.URL.Path) {
		if started {
			writeJSON(w, http.StatusAccepted, map[string]string{"outcome": "started"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"outcome": "already"})
		return
	}
	code := "refreshing"
	if !started {
		code = "already_refreshing"
	}
	w.Header().Set("Location", "/usage?done="+code)
	w.WriteHeader(http.StatusSeeOther)
}

// usageToast is the toast on the usage screen after a refresh POST. Only the
// two codes that POST leads to count, so another screen's code shows nothing
// here.
func usageToast(code string) string {
	if !refreshToastCodes[code] {
		return ""
	}
	return toastTexts[code]
}

// refreshToastCodes are the toast codes that belong to the usage screen.
// toastFor leaves them out so another screen's address cannot show them.
var refreshToastCodes = map[string]bool{"refreshing": true, "already_refreshing": true}

// usagePage is the usage screen's layout of a UsageView: what the template
// needs that is a decision rather than a field, so the template holds no
// logic beyond choosing between these.
type usagePage struct {
	// Badge is the stored decision as a badge; zero when none is recorded.
	Badge badgeView
	// Meters is one per stored window, in the stored order.
	Meters []meterView
}

// meterView is one usage window drawn as a design-system meter.
type meterView struct {
	// Name is the window's kind and scope, e.g. "Weekly (all)".
	Name string
	// Value is the words at the right of the name, e.g. "88% used".
	Value string
	// Label is the progress bar's accessible name.
	Label string
	// Now is the whole percent the bar shows, rounded and held to 0-100:
	// aria-valuenow and the fill's width class both use it.
	Now int
	// Class is the meter's state class: "", "meter--warn" or "meter--full".
	Class string
	// ResetsAt is nil when the reset time is not known.
	ResetsAt *time.Time
}

// Thresholds for a meter's colour. The colour is never alone: the number is
// always printed beside it.
const (
	meterWarnAt = 80.0
	meterFullAt = 100.0
)

// usagePageFor lays out a view for the page. Pure.
func usagePageFor(v UsageView) usagePage {
	p := usagePage{Meters: make([]meterView, 0, len(v.Windows))}
	if v.Governor != nil {
		p.Badge = governorBadge(v.Governor.Action)
	}
	for _, w := range v.Windows {
		p.Meters = append(p.Meters, meterFor(w))
	}
	return p
}

// governorBadge names a stored decision in plain words. A word this build
// does not know is shown as stored, in a plain badge, because the file may
// come from a newer afk and hiding the word would hide the decision.
func governorBadge(action string) badgeView {
	switch action {
	case "run":
		return badgeView{Class: "badge--running", Word: "Run"}
	case "no_launch":
		return badgeView{Class: "badge--held", Word: "Launch nothing new"}
	case "pause":
		return badgeView{Class: "badge--paused", Word: "Pause"}
	case "hold":
		return badgeView{Class: "badge--idle", Word: "Hold"}
	case "refuse":
		return badgeView{Class: "badge--failed", Word: "Refuse"}
	}
	return badgeView{Class: "badge--plain", Word: action}
}

// meterFor draws one window. The colour state comes from the real figure and
// the width from the rounded, clamped one, so 99.6 is still "warn" and not
// "full" while its bar reads 100.
func meterFor(w usagestate.Window) meterView {
	now := int(math.Round(w.UsedPct))
	if now < 0 {
		now = 0
	}
	if now > 100 {
		now = 100
	}
	name := windowName(w)
	m := meterView{
		Name:     name,
		Value:    fmt.Sprintf("%.0f%% used", w.UsedPct),
		Label:    name + " usage",
		Now:      now,
		ResetsAt: w.ResetsAt,
	}
	switch {
	case w.UsedPct >= meterFullAt:
		m.Class = "meter--full"
	case w.UsedPct >= meterWarnAt:
		m.Class = "meter--warn"
	}
	return m
}

// windowName is a window's kind with a capital, and its scope in brackets.
func windowName(w usagestate.Window) string {
	name := w.Kind
	if name != "" {
		name = strings.ToUpper(name[:1]) + name[1:]
	}
	if w.Scope != "" {
		name += " (" + w.Scope + ")"
	}
	return name
}
