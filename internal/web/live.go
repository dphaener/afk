package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/planchat"
	"github.com/dphaener/afk/internal/plansession"
	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/usagestate"
	"github.com/dphaener/afk/internal/webpush"
)

// This file is the live-change stream: GET /events, a server-sent events
// stream that tells an open page which part of which project has changed, so
// the page can fetch itself again and swap that part in place instead of
// waiting for the owner to reload.
//
// The stream carries names, never content. A browser that hears "the inbox of
// app changed" asks for the page again through the ordinary, sign-in-guarded
// handlers, which read the files fresh as they always do. So the stream adds
// no second way to read anything, and what the page shows still comes from
// one place.
//
// One reader serves every open stream. It reads each project's files once per
// tick and compares the result with the tick before; ten open pages cost one
// read per tick, not ten. It never waits for a browser: a stream whose queue
// is full is dropped (the browser reconnects and fetches the whole page once),
// copying how internal/host drops a slow client rather than let it pin the
// PTY pump. A phone on a bad line must not hold up the page for everyone else.

// The parts of a project an event can name. These strings are the wire names
// a page's script matches against, so they must not change.
const (
	partInbox    = "inbox"
	partLedger   = "ledger"
	partPause    = "pause"
	partSessions = "sessions"
	partPlanning = "planning"
	partUsage    = "usage"
	partDevices  = "devices"
	// partScreen is a watched session's screen (T17.14). It is not in parts:
	// it is not read from files on every tick but from Rex, only while a
	// watcher's lease is live (watchTick), and its change names a session.
	partScreen = "screen"
)

// parts is every part in the order events are sent within one project.
var parts = []string{partInbox, partLedger, partPause, partSessions, partPlanning, partUsage, partDevices}

// machineParts is how many of parts are machine-wide (usage and devices)
// rather than per project.
const machineParts = 2

// machineProject is the project key of the machine-wide parts (usage and
// devices). There is one usage reading and one device list per machine, so
// their events name no project.
const machineProject = ""

// The stream's limits.
const (
	// liveQueue is how many messages one stream may have waiting. A tick
	// sends at most one event per part per project, so a healthy browser
	// never comes close; a queue that fills means the browser has stopped
	// reading, and it is dropped.
	liveQueue = 16
	// liveMaxStreams is how many streams may be open at once. Each one holds
	// a goroutine and a socket for as long as the page is open; the owner
	// has a phone and a laptop, and 32 leaves room for many tabs while
	// keeping a runaway reconnect loop from holding the daemon's sockets.
	liveMaxStreams = 32
	// liveTick is how often the reader looks at the files. It is not a
	// setting: the dashboard re-reads on the same two seconds, and §3 of the
	// plan forbids reading faster than that.
	liveTick = 2 * time.Second
	// livePingEvery is how many ticks pass between keep-alive comments.
	// Twelve ticks is about 24 seconds, under the 30 to 60 seconds after
	// which common proxies close a connection that has said nothing.
	livePingEvery = 12
	// liveWriteLimit is how long one write to a browser may take. It stands
	// in for the server's 30 s write timeout, which would otherwise end every
	// stream after 30 seconds (see events).
	liveWriteLimit = 5 * time.Second
	// liveRetryMS is what the stream tells the browser to wait before it
	// reconnects after the stream ends.
	liveRetryMS = 3000
)

// absent is what a planning file's fingerprint holds when the file does not
// exist, so that "missing" and "empty" are told apart.
const absent = "\x00absent"

// change is one event: this part of this project is different from the last
// tick. The JSON is the event's data on the wire.
//
// Session is set only for the screen part, which is per session; omitempty
// keeps every other part's wire form exactly as it was before the Watch view.
type change struct {
	Project string `json:"project"`
	Part    string `json:"part"`
	Session string `json:"session,omitempty"`
}

// snapshot is one tick's reading: project name to part to fingerprint. The
// machine-wide parts (usage and devices) are under machineProject.
type snapshot map[string]map[string]string

// fingerprint is the hex SHA-256 of the JSON of what was read, with the read
// error's text folded in. Folding the error in is what makes "it could be
// read, now it cannot" a change while the same failure twice is not.
func fingerprint(value any, err error) string {
	v := struct {
		Value any    `json:"value"`
		Err   string `json:"err,omitempty"`
	}{Value: value}
	if err != nil {
		v.Err = err.Error()
	}
	data, mErr := json.Marshal(v)
	if mErr != nil {
		// Nothing read here holds a value json cannot encode (no channels,
		// no functions, no NaN). Should one ever appear, the encoding error
		// itself is the fingerprint: stable while nothing changes.
		data = []byte(mErr.Error())
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// readSnapshot reads every part of every project, and the machine's usage
// and device list, once. It is the only place the stream touches the disk.
// It never blocks: every read is a plain file read or a Deps function that
// must not block.
//
// A project listed twice under one name is read twice and the later reading
// kept; the daemon's list drops duplicates, so this does not happen there.
//
// refreshGen is the usage refresher's generation (usageRefresher.generation),
// folded into the usage part: a reading starting, landing or failing each
// changes it, and a failure writes no file, so the file alone would never
// tell an open page that its Refreshing button should stop.
func readSnapshot(projects []Project, d Deps, refreshGen uint64) snapshot {
	snap := make(snapshot, len(projects)+1)
	for _, p := range projects {
		snap[p.Name] = readProject(p.Name, p.Root, d)
	}
	var usage usagestate.File
	var ok bool
	if d.Usage != nil {
		usage, ok = d.Usage()
	}
	snap[machineProject] = map[string]string{partUsage: fingerprint(struct {
		File    usagestate.File `json:"file"`
		OK      bool            `json:"ok"`
		Refresh uint64          `json:"refresh"`
	}{usage, ok, refreshGen}, nil)}
	// The signed-up devices are machine-wide like usage: push-devices.json
	// sits in the machine's state folder and every project's notices go to
	// every device on it, so there is one list for the whole page and its
	// events name no project. With push off there is no list to watch, and
	// the part is left out rather than fingerprinted as empty, so turning
	// push on mid-run is not taken for a change. The fingerprint is of the
	// whole file, endpoints included; it is a hash and never leaves the
	// server, and a re-subscribe that changes only the keys also changes
	// the time, which the list shows.
	if d.PushDir != "" {
		snap[machineProject][partDevices] = fingerprint(webpush.ReadDevices(d.PushDir), nil)
	}
	return snap
}

// withBeingPlanned adds the projects being planned to the configured ones,
// leaving out any whose name a configured project already has.
func withBeingPlanned(projects []Project, planned []BeingPlannedView) []Project {
	out := append([]Project(nil), projects...)
	for _, bp := range planned {
		if _, dup := named(projects, bp.Name); !dup {
			out = append(out, Project{Name: bp.Name, Root: bp.Root})
		}
	}
	return out
}

// readProject reads one project's parts, each from the same files the page
// that shows it reads.
func readProject(name, root string, d Deps) map[string]string {
	fp := make(map[string]string, len(parts)-machineParts)

	items, unreadable, err := inbox.Store{Dir: inbox.Dir(root), Now: d.Now}.List()
	fp[partInbox] = fingerprint(struct {
		Items      []inbox.Item `json:"items"`
		Unreadable []string     `json:"unreadable"`
	}{items, unreadable}, err)

	l, err := ledger.Load(root)
	fp[partLedger] = fingerprint(l, err)

	// The pause part is everything the project page's pause line and run
	// state are built from: the pause flag, the stop marker and daemon.json.
	stopTask, stopReason, stopped := daemonstate.ReadStop(root)
	state, stateOK := daemonstate.ReadState(root)
	fp[partPause] = fingerprint(struct {
		Paused     bool              `json:"paused"`
		StopTask   string            `json:"stop_task"`
		StopReason string            `json:"stop_reason"`
		Stopped    bool              `json:"stopped"`
		State      daemonstate.State `json:"state"`
		StateOK    bool              `json:"state_ok"`
	}{daemonstate.Paused(root), stopTask, stopReason, stopped, state, stateOK}, nil)

	var sessions []SessionView
	if d.Sessions != nil {
		sessions = d.Sessions(root)
	}
	fp[partSessions] = fingerprint(sessions, nil)

	fp[partPlanning] = fingerprint(planningPart(name, root, d), nil)
	return fp
}

// planningPart is what the planning part's fingerprint is made of: the raw
// planning files, and, when the page has a conversation reader, the
// conversation.
//
// The conversation is part of planning (T8.12) because the chat screen is
// drawn from it: a new agent message, a question opening or a state change
// (working to waiting for a choice) must reach an open chat screen, and none
// of them writes a file in planningFiles' list; the transcript lives under
// the agent's own folder. Only State, Messages, Questions and Problem are
// folded in. Project is the name already keyed on, and nothing that changes
// every tick (a clock, a read count) may ever be added, or the screen would
// swap on every poll. It is read afresh on every tick, never cached, so a
// change is seen on the tick it happens; planchat.Read answers none without
// reading a transcript unless the host is alive, which bounds the cost. A
// nil d.PlanChat leaves the part as the files alone.
func planningPart(name, root string, d Deps) any {
	files := planningFiles(root)
	if d.PlanChat == nil {
		return files
	}
	c := d.PlanChat(name, root)
	return struct {
		Files     []string            `json:"files"`
		State     planchat.State      `json:"state"`
		Messages  []planchat.Message  `json:"messages"`
		Questions []provider.Question `json:"questions"`
		Problem   string              `json:"problem"`
	}{files, c.State, c.Messages, c.Questions, c.Problem}
}

// planningFiles reads the raw bytes of the files a planning session changes
// as it goes, in a fixed order, with absent for a missing file and the error
// text for one that cannot be read.
//
// The paths are §4.8 of docs/PLAN.md and the T5.3 amendment: the state is in
// .afk/plan/ (plansession.Dir), and the agent runs as the session named
// "master-plan", whose host.json and status.json are in .afk/master-plan/.
// questions.jsonl and the master-plan folder are written out literally
// because the packages that own them do not export the names
// (internal/plansession keeps its file names private) or would pull the
// whole session and PTY machinery into the page for one constant
// (internal/session's PlanSessionName).
func planningFiles(root string) []string {
	paths := []string{
		plansession.StatePath(root),
		filepath.Join(plansession.Dir(root), "questions.jsonl"),
		filepath.Join(root, ".afk", "master-plan", "host.json"),
		filepath.Join(root, ".afk", "master-plan", "status.json"),
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		switch {
		case err == nil:
			out = append(out, string(data))
		case errors.Is(err, os.ErrNotExist):
			out = append(out, absent)
		default:
			out = append(out, "\x00error: "+err.Error())
		}
	}
	return out
}

// changedParts decides one tick's events from the tick before and this one.
// Pure.
//
// Only a part whose fingerprint differs counts. A project seen for the first
// time sends nothing (there is nothing to compare it with, and a page that
// shows it was fetched after it appeared or will be on its next reload), nor
// does one that has gone. An empty prev is the reader's first tick, a
// baseline, and sends nothing for the same reason.
//
// The result is sorted by project, then by the order of parts, so a page sees
// the same order every time and tests can compare whole lists.
func changedParts(prev, cur snapshot) []change {
	if len(prev) == 0 {
		return nil
	}
	names := make([]string, 0, len(cur))
	for name := range cur {
		if _, ok := prev[name]; ok {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	var out []change
	for _, name := range names {
		before, after := prev[name], cur[name]
		for _, part := range parts {
			a, inA := before[part]
			b, inB := after[part]
			if inA && inB && a != b {
				out = append(out, change{Project: name, Part: part})
			}
		}
	}
	return out
}

// liveMsg is one item in a stream's queue: a change, or a keep-alive ping.
type liveMsg struct {
	change change
	ping   bool
}

// subscriber is one open stream's queue.
type subscriber struct {
	ch chan liveMsg
}

// hub is the shared reader and the streams it feeds. There is one per server.
//
// The reader runs only while a stream is open: it starts with the first and
// stops when the last one goes, so a daemon nobody is looking at reads
// nothing extra.
type hub struct {
	d Deps
	// refreshGen reads the usage refresher's generation each tick (see
	// readSnapshot). Nil reads as zero.
	refreshGen func() uint64

	mu   sync.Mutex
	subs map[*subscriber]struct{}
	// quit is the running reader's stop signal, or nil when none runs. Each
	// reader gets its own, so a reader that is stopping cannot be confused
	// with one started after it.
	quit chan struct{}

	// watch is who is watching which session's screen. It guards itself;
	// the server's ping renews it and the reader only counts it.
	watch watchLeases
	// screens is the reader's memory of each watched session's screen, by
	// watchKey, guarded by mu. An entry lives while its session is watched
	// or a read of it is still out.
	screens map[string]*watchScreen
	// spawn runs one screen read off the reader's goroutine. It is go f()
	// in real use; a test runs f at once so a tick's reads are done when
	// the tick is.
	spawn func(f func())
}

func newHub(d Deps, refreshGen func() uint64) *hub {
	return &hub{
		d:          d,
		refreshGen: refreshGen,
		subs:       make(map[*subscriber]struct{}),
		screens:    make(map[string]*watchScreen),
		spawn:      func(f func()) { go f() },
	}
}

// subscribe opens a queue for one stream. It returns false when liveMaxStreams
// are already open. The returned function is safe to call more than once, and
// after the hub has already dropped the stream.
func (h *hub) subscribe() (<-chan liveMsg, func(), bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.subs) >= liveMaxStreams {
		return nil, nil, false
	}
	sub := &subscriber{ch: make(chan liveMsg, liveQueue)}
	h.subs[sub] = struct{}{}
	if h.quit == nil {
		h.quit = make(chan struct{})
		go h.run(h.quit)
	}
	return sub.ch, func() { h.unsubscribe(sub) }, true
}

func (h *hub) unsubscribe(sub *subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs, sub)
	if len(h.subs) == 0 && h.quit != nil {
		close(h.quit)
		h.quit = nil
	}
}

// ticker returns the reader's tick source: the injected one, or a real
// liveTick ticker.
func (h *hub) ticker() (<-chan time.Time, func()) {
	if h.d.Ticker != nil {
		return h.d.Ticker()
	}
	t := time.NewTicker(liveTick)
	return t.C, t.Stop
}

// run is the reader. Each tick it reads one snapshot, works out the changes,
// and hands them to every stream without waiting for any.
func (h *hub) run(quit chan struct{}) {
	tick, stop := h.ticker()
	defer stop()
	var last snapshot
	for n := 1; ; n++ {
		select {
		case <-quit:
			return
		case <-tick:
		}
		var projects []Project
		if h.d.Projects != nil {
			projects = h.d.Projects()
		}
		// A project being planned has a live chat but no project row, so its
		// planning part is read too. A configured name wins, as it does for
		// the address (findChatProject).
		if h.d.BeingPlanned != nil {
			projects = withBeingPlanned(projects, h.d.BeingPlanned())
		}
		var gen uint64
		if h.refreshGen != nil {
			gen = h.refreshGen()
		}
		cur := readSnapshot(projects, h.d, gen)
		changes := changedParts(last, cur)
		last = cur

		h.mu.Lock()
		select {
		case <-quit:
			// The last stream went while the files were being read. A
			// stream opened since then belongs to the next reader.
			h.mu.Unlock()
			return
		default:
		}
		for _, c := range changes {
			h.fanOut(liveMsg{change: c})
		}
		if n%livePingEvery == 0 {
			h.fanOut(liveMsg{ping: true})
		}
		h.mu.Unlock()
		now := time.Now
		if h.d.Now != nil {
			now = h.d.Now
		}
		h.watchTick(projects, now())
	}
}

// watchReadLimit bounds one screen read. A Rex server that hangs must not
// keep a read out for ever: while one is out no other read of that session
// starts, so a hung read would freeze the watch until the daemon restarts.
// Thirty seconds is far above a busy block's 200-350 ms render (T17.1).
const watchReadLimit = 30 * time.Second

// watchScreen is what the reader remembers of one watched session's screen
// between ticks: the inputs watchReadDue decides from, whether a read is
// out, and the last reading sent.
type watchScreen struct {
	project, session string
	lastRead         time.Time
	lastTotal        int64
	lastFailed       bool
	inFlight         bool
	// hash is the hex SHA-256 of the last HTML a change was sent for, so the
	// same screen read twice sends nothing.
	hash string
	// reading is the last good reading. The screen route can serve it
	// rather than read Rex a second time for the change it just heard of;
	// it is the reader's own last reading, kept only while someone watches,
	// not a copy of a file, so a page never sees an older file through it.
	reading WatchReading
	ok      bool
}

// watchKey is one watched session's key in the lease registry and in
// h.screens. The NUL cannot appear in a project or session name, so two
// different pairs never share a key.
func watchKey(project, session string) string {
	return project + "\x00" + session
}

// watchedKeys lists the keys with at least one live lease. It reads the
// registry's map under its own lock, because watch.go's registry only
// counts one key at a time and the reader must find every watched key.
func (l *watchLeases) watchedKeys(now time.Time) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for key, ws := range l.leases {
		for _, last := range ws {
			if now.Before(last.Add(watchLease)) {
				out = append(out, key)
				break
			}
		}
	}
	slices.Sort(out)
	return out
}

// watchTick starts this tick's screen reads. It runs on the reader's
// goroutine after the file changes are sent and must never wait on Rex: it
// only decides and hands each read to spawn. A session is read only while a
// lease on it is live, at most once at a time, and when watchReadDue says
// so; with no watcher it reads nothing at all, the plan's "never read for
// nobody". A session whose project is not on this tick's list is skipped,
// since there is no folder to read it from.
func (h *hub) watchTick(projects []Project, now time.Time) {
	if h.d.WatchScreen == nil {
		return
	}
	h.watch.Prune(now)
	keys := h.watch.watchedKeys(now)
	roots := make(map[string]string, len(projects))
	for _, p := range projects {
		roots[p.Name] = p.Root
	}

	type job struct{ key, root, project, session string }
	var jobs []job
	h.mu.Lock()
	watched := make(map[string]bool, len(keys))
	for _, key := range keys {
		watched[key] = true
		project, session, _ := strings.Cut(key, "\x00")
		root, ok := roots[project]
		if !ok {
			continue
		}
		st := h.screens[key]
		if st == nil {
			st = &watchScreen{project: project, session: session, lastTotal: -1}
			h.screens[key] = st
		}
		if st.inFlight {
			continue
		}
		due, _ := watchReadDue(WatchInputs{
			Watchers:   h.watch.Count(key, now),
			LastRead:   st.lastRead,
			Now:        now,
			LastTotal:  st.lastTotal,
			LastFailed: st.lastFailed,
		})
		if !due {
			continue
		}
		st.inFlight = true
		st.lastRead = now
		jobs = append(jobs, job{key, root, project, session})
	}
	// A session nobody watches any more is forgotten, so a watcher who
	// comes back is read at once and gets a change for the first frame. One
	// with a read still out is kept until the read settles.
	for key, st := range h.screens {
		if !watched[key] && !st.inFlight {
			delete(h.screens, key)
		}
	}
	h.mu.Unlock()

	for _, j := range jobs {
		h.spawn(func() {
			ctx, cancel := context.WithTimeout(context.Background(), watchReadLimit)
			defer cancel()
			r, err := h.d.WatchScreen(ctx, j.root, j.session, watchBound)
			h.settleWatch(j.key, r, err)
		})
	}
}

// settleWatch records one finished screen read and sends a screen change
// when the HTML differs from the last one sent. A failed read sends
// nothing: the page keeps the frame it has, and watchReadDue retries on the
// slow cadence, so a gone block costs Rex one call every watchSlowEvery.
func (h *hub) settleWatch(key string, r WatchReading, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := h.screens[key]
	if st == nil {
		return
	}
	st.inFlight = false
	if err != nil {
		st.lastFailed = true
		return
	}
	st.lastFailed = false
	st.lastTotal = r.Total
	sum := sha256.Sum256(r.HTML)
	hash := hex.EncodeToString(sum[:])
	st.reading, st.ok = r, true
	if hash == st.hash {
		return
	}
	st.hash = hash
	h.fanOut(liveMsg{change: change{Project: st.project, Part: partScreen, Session: st.session}})
}

// watchFrame is the last good reading of a watched session's screen, and
// false when there is none yet (nobody watches it, or every read failed).
func (h *hub) watchFrame(project, session string) (WatchReading, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := h.screens[watchKey(project, session)]
	if st == nil || !st.ok {
		return WatchReading{}, false
	}
	return st.reading, true
}

// fanOut hands msg to every stream without blocking. A stream whose queue is
// full is closed and forgotten: its browser has stopped reading, and waiting
// for it would delay every other page. Its handler sees the closed queue and
// ends the response; the browser reconnects and fetches the page once, so
// nothing it missed stays missed. The caller holds h.mu.
func (h *hub) fanOut(msg liveMsg) {
	for sub := range h.subs {
		select {
		case sub.ch <- msg:
		default:
			close(sub.ch)
			delete(h.subs, sub)
		}
	}
	if len(h.subs) == 0 && h.quit != nil {
		close(h.quit)
		h.quit = nil
	}
}

// events is GET /events. It is mounted on the mux by itself and not through
// register, because a stream has no JSON form and must not get an /api twin;
// it is still behind guard, like everything else.
//
// The server's WriteTimeout (30 s in afk daemon) is a deadline on the whole
// response, so it would cut every stream after 30 seconds. The handler clears
// it with http.ResponseController and sets its own short deadline before each
// write instead: a browser that stops reading is still let go, after
// liveWriteLimit rather than never. The deadlines are taken from the real
// clock and not Deps.Now, because they are about the socket, not about
// anything the page shows.
func (s *server) events(w http.ResponseWriter, r *http.Request) {
	queue, unsubscribe, ok := s.hub.subscribe()
	if !ok {
		s.fail(w, r, http.StatusServiceUnavailable, "Too many pages with live updates are open at once. Close a few tabs, or reload this page to see the latest.")
		return
	}
	defer unsubscribe()

	rc := http.NewResponseController(w)
	// ErrNotSupported (a writer with no connection behind it, as in a test)
	// means there is no deadline to clear; anything else is ignored too,
	// because the per-write deadline below fails the stream if it matters.
	_ = rc.SetWriteDeadline(time.Time{})

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	// nginx buffers a proxied response unless told not to, which would hold
	// every event until its buffer filled.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	write := func(text string) bool {
		if err := rc.SetWriteDeadline(time.Now().Add(liveWriteLimit)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return false
		}
		if _, err := w.Write([]byte(text)); err != nil {
			return false
		}
		if err := rc.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return false
		}
		return true
	}

	if !write("retry: " + strconv.Itoa(liveRetryMS) + "\n\n") {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case msg, open := <-queue:
			if !open {
				// Dropped by the hub for falling behind.
				return
			}
			if !write(formatLive(msg)) {
				return
			}
		}
	}
}

// formatLive is one message as it goes on the wire. Pure.
func formatLive(msg liveMsg) string {
	if msg.ping {
		return ": ping\n\n"
	}
	data, err := json.Marshal(msg.change)
	if err != nil {
		// Two strings always encode.
		return ": ping\n\n"
	}
	var b strings.Builder
	b.WriteString("event: change\ndata: ")
	b.Write(data)
	b.WriteString("\n\n")
	return b.String()
}
