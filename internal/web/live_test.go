package web

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/usagestate"
)

// liveFixture is two temp-folder projects ("app" and "other") and the fake
// session and usage readings the snapshot asks Deps for.
type liveFixture struct {
	t        *testing.T
	app      string
	other    string
	projects []Project
	sessions []SessionView
	usage    usagestate.File
	// refreshGen stands in for the usage refresher's generation.
	refreshGen uint64
}

func newLiveFixture(t *testing.T) *liveFixture {
	t.Helper()
	fx := &liveFixture{t: t, app: t.TempDir(), other: t.TempDir()}
	fx.projects = []Project{{Name: "app", Root: fx.app}, {Name: "other", Root: fx.other}}
	fx.sessions = []SessionView{{Name: "t1-2", Phase: "executing", HostRunning: true}}
	fx.usage = usagestate.File{At: testNow, Source: "probe", Windows: []usagestate.Window{{Kind: "session", UsedPct: 10}}}
	return fx
}

func (fx *liveFixture) deps() Deps {
	return Deps{
		Projects: func() []Project { return fx.projects },
		Sessions: func(root string) []SessionView {
			if root != fx.app {
				return nil
			}
			return fx.sessions
		},
		Usage: func() (usagestate.File, bool) { return fx.usage, true },
		Now:   func() time.Time { return testNow },
	}
}

func (fx *liveFixture) read() snapshot {
	return readSnapshot(fx.projects, fx.deps(), fx.refreshGen)
}

func (fx *liveFixture) write(root, rel, text string) {
	fx.t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fx.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		fx.t.Fatal(err)
	}
}

func (fx *liveFixture) addItem(root, id, title string) {
	fx.t.Helper()
	st := inbox.Store{Dir: inbox.Dir(root), Now: func() time.Time { return testNow }, NewID: func() string { return id }}
	if _, err := st.Add(inbox.NewItem{Kind: "notice", Title: title}); err != nil {
		fx.t.Fatal(err)
	}
}

func (fx *liveFixture) initLedger(root string) {
	fx.t.Helper()
	if err := ledger.Init(root, []string{"T1.1"}); err != nil {
		fx.t.Fatal(err)
	}
}

// damagedLedger is text ledger.Load cannot decode.
const damagedLedger = "{this is not a ledger"

// TestChangedParts reads two snapshots from real files around one edit and
// checks the events changedParts decides from them.
//
// Load-bearing rows:
//   - one row per part: each proves the snapshot reads the file the page
//     shows that part from; a part whose read was missed would never send.
//   - "first tick": the reader's first reading is a baseline. Without the
//     row a page opening its stream would get an event for every part of
//     every project and fetch itself for nothing.
//   - the appear/disappear rows: a project added to the config mid-run must
//     not send (there is nothing to compare it with), nor crash.
//   - the damaged-ledger rows: a read error is part of the fingerprint, so
//     damage then repair is a change, and the same damage twice is not (a
//     project stuck with a bad file must not send every two seconds).
//   - "sorted": the order is what lets a page and a test rely on it.
func TestChangedParts(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(fx *liveFixture)
		edit    func(fx *liveFixture)
		noPrev  bool
		want    []change
		wantNil bool
	}{
		{
			name:    "no change",
			edit:    func(*liveFixture) {},
			wantNil: true,
		},
		{
			name: "inbox: an item is added",
			edit: func(fx *liveFixture) { fx.addItem(fx.app, "abc234", "Which database?") },
			want: []change{{"app", partInbox}},
		},
		{
			name:  "inbox: an item becomes unreadable",
			setup: func(fx *liveFixture) { fx.addItem(fx.app, "abc234", "Which database?") },
			edit:  func(fx *liveFixture) { fx.write(fx.app, ".afk/inbox/abc234.json", "{") },
			want:  []change{{"app", partInbox}},
		},
		{
			name:    "inbox: an unreadable item stays unreadable",
			setup:   func(fx *liveFixture) { fx.write(fx.app, ".afk/inbox/abc234.json", "{") },
			edit:    func(fx *liveFixture) { fx.write(fx.app, ".afk/inbox/abc234.json", "{") },
			wantNil: true,
		},
		{
			name: "ledger: the ledger is written",
			edit: func(fx *liveFixture) { fx.initLedger(fx.app) },
			want: []change{{"app", partLedger}},
		},
		{
			name:  "ledger: a damaged ledger becomes readable",
			setup: func(fx *liveFixture) { fx.write(fx.app, ".afk/build/ledger.json", damagedLedger) },
			edit: func(fx *liveFixture) {
				if err := os.Remove(ledger.Path(fx.app)); err != nil {
					t.Fatal(err)
				}
				fx.initLedger(fx.app)
			},
			want: []change{{"app", partLedger}},
		},
		{
			name:  "ledger: a readable ledger is damaged",
			setup: func(fx *liveFixture) { fx.initLedger(fx.app) },
			edit:  func(fx *liveFixture) { fx.write(fx.app, ".afk/build/ledger.json", damagedLedger) },
			want:  []change{{"app", partLedger}},
		},
		{
			name:    "ledger: a damaged ledger stays damaged",
			setup:   func(fx *liveFixture) { fx.write(fx.app, ".afk/build/ledger.json", damagedLedger) },
			edit:    func(fx *liveFixture) { fx.write(fx.app, ".afk/build/ledger.json", damagedLedger) },
			wantNil: true,
		},
		{
			name: "pause: the project is paused",
			edit: func(fx *liveFixture) {
				if err := daemonstate.Pause(fx.app, testNow); err != nil {
					t.Fatal(err)
				}
			},
			want: []change{{"app", partPause}},
		},
		{
			name: "pause: the daemon records a new run state",
			edit: func(fx *liveFixture) {
				if err := daemonstate.WriteState(fx.app, daemonstate.State{Kind: "launched", Task: "T1.2"}); err != nil {
					t.Fatal(err)
				}
			},
			want: []change{{"app", partPause}},
		},
		{
			name: "sessions: a session's phase moves",
			edit: func(fx *liveFixture) {
				fx.sessions = []SessionView{{Name: "t1-2", Phase: "verifying", HostRunning: true}}
			},
			want: []change{{"app", partSessions}},
		},
		{
			name: "planning: the plan session's state is written",
			edit: func(fx *liveFixture) { fx.write(fx.app, ".afk/plan/state.json", `{"phase":"brief"}`) },
			want: []change{{"app", partPlanning}},
		},
		{
			name: "planning: the planning host starts",
			edit: func(fx *liveFixture) { fx.write(fx.app, ".afk/master-plan/host.json", `{"pid":1}`) },
			want: []change{{"app", partPlanning}},
		},
		{
			name:  "planning: an empty questions file is not a missing one",
			setup: func(fx *liveFixture) { fx.write(fx.app, ".afk/plan/brief.md", "x") },
			edit:  func(fx *liveFixture) { fx.write(fx.app, ".afk/plan/questions.jsonl", "") },
			want:  []change{{"app", partPlanning}},
		},
		{
			name: "usage: a new reading, machine-wide",
			edit: func(fx *liveFixture) {
				fx.usage.Windows = []usagestate.Window{{Kind: "session", UsedPct: 55}}
			},
			want: []change{{"", partUsage}},
		},
		{
			// Load-bearing: a failed reading writes no file, so the
			// refresher's generation is the only thing that changes. Without
			// it in the fingerprint an open page would show Refreshing
			// forever after a failure.
			name: "usage: the refresher's generation changes, with no file change",
			edit: func(fx *liveFixture) { fx.refreshGen++ },
			want: []change{{"", partUsage}},
		},
		{
			name:    "first tick",
			edit:    func(fx *liveFixture) { fx.addItem(fx.app, "abc234", "Which database?") },
			noPrev:  true,
			wantNil: true,
		},
		{
			name: "a project appears",
			edit: func(fx *liveFixture) {
				fresh := t.TempDir()
				fx.addItem(fresh, "abc234", "Hello")
				fx.projects = append(fx.projects, Project{Name: "fresh", Root: fresh})
			},
			wantNil: true,
		},
		{
			name: "a project goes",
			edit: func(fx *liveFixture) {
				fx.addItem(fx.other, "abc234", "Hello")
				fx.projects = fx.projects[:1]
			},
			wantNil: true,
		},
		{
			name: "sorted: changes in two projects and the machine",
			edit: func(fx *liveFixture) {
				fx.addItem(fx.other, "abc234", "Hello")
				fx.initLedger(fx.app)
				fx.addItem(fx.app, "def567", "Hi")
				fx.usage.Source = "statusline"
			},
			want: []change{{"", partUsage}, {"app", partInbox}, {"app", partLedger}, {"other", partInbox}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newLiveFixture(t)
			if tc.setup != nil {
				tc.setup(fx)
			}
			prev := fx.read()
			if tc.noPrev {
				prev = nil
			}
			tc.edit(fx)
			got := changedParts(prev, fx.read())
			if tc.wantNil {
				if got != nil {
					t.Fatalf("changedParts = %v, want no change", got)
				}
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("changedParts = %v, want %v", got, tc.want)
			}
		})
	}
}

// liveRig is a handler over the fixture's projects with a ticker the test
// drives: the reader looks at the files exactly when tick is called.
type liveRig struct {
	*liveFixture
	h     http.Handler
	ticks chan time.Time
	// srv is the server behind h. gate holds the fake usage reading until
	// release, which then fails, as a reading that writes no file does.
	srv  *server
	gate chan struct{}
	once sync.Once
}

// release lets the fake usage reading finish. Safe to call more than once.
func (rg *liveRig) release() { rg.once.Do(func() { close(rg.gate) }) }

func newLiveRig(t *testing.T, extra int) *liveRig {
	t.Helper()
	rg := &liveRig{liveFixture: newLiveFixture(t), ticks: make(chan time.Time), gate: make(chan struct{})}
	for i := range extra {
		rg.projects = append(rg.projects, Project{Name: "p" + string(rune('a'+i)), Root: t.TempDir()})
	}
	d := rg.deps()
	d.LoadBuild = func(string) ([]masterplan.Task, ledger.Ledger, error) { return nil, ledger.Ledger{}, nil }
	d.Pause = func(string) error { return nil }
	d.Resume = func(string) error { return nil }
	d.Ticker = func() (<-chan time.Time, func()) { return rg.ticks, func() {} }
	d.RefreshUsage = func() UsageRefreshResult {
		<-rg.gate
		return UsageRefreshResult{Outcome: UsageRefreshFailed, Text: "The reading timed out."}
	}
	srv, h, err := build(Config{Token: testToken}, d)
	if err != nil {
		t.Fatal(err)
	}
	rg.h, rg.srv = h, srv
	t.Cleanup(func() {
		rg.release()
		srv.refresher.wait()
	})
	return rg
}

// tick lets the reader take one reading. The channel is unbuffered, so the
// send returns once the reader has finished every earlier tick.
func (rg *liveRig) tick() {
	rg.t.Helper()
	select {
	case rg.ticks <- testNow:
	case <-time.After(10 * time.Second):
		rg.t.Fatal("the reader did not take a tick within 10 s")
	}
}

// syncPing sends livePingEvery ticks and waits for the ping that ends them on
// st. The reader sends a tick's changes before its ping, so once the ping is
// read every one of those ticks has been read and handed out, and anything
// before it is on lines.
func (rg *liveRig) syncPing(st *stream) []string {
	rg.t.Helper()
	for range livePingEvery {
		rg.tick()
	}
	before := st.until(": ping")
	st.until("") // the blank line that ends the ping
	return before
}

// stream is one open GET /events read line by line on a goroutine.
type stream struct {
	t     *testing.T
	resp  *http.Response
	lines chan string
}

func openStream(t *testing.T, srv *httptest.Server) *stream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/events", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	st := &stream{t: t, resp: resp, lines: make(chan string, 256)}
	go func() {
		defer close(st.lines)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			st.lines <- sc.Text()
		}
	}()
	return st
}

// until reads lines up to and including want and returns the ones before
// it, failing after 10 s or when the stream ends first.
func (st *stream) until(want string) []string {
	st.t.Helper()
	var seen []string
	deadline := time.After(10 * time.Second)
	for {
		select {
		case line, ok := <-st.lines:
			if !ok {
				st.t.Fatalf("the stream ended before %q; it sent %q", want, seen)
			}
			if line == want {
				return seen
			}
			seen = append(seen, line)
		case <-deadline:
			st.t.Fatalf("no %q within 10 s; the stream sent %q", want, seen)
		}
	}
}

// events is the change lines among lines.
func events(lines []string) []string {
	var out []string
	for _, l := range lines {
		if strings.HasPrefix(l, "event:") || strings.HasPrefix(l, "data:") {
			out = append(out, l)
		}
	}
	return out
}

// TestEvents exercises GET /events over a real HTTP connection.
//
// Load-bearing: "no token" (the stream says what changed in the owner's
// projects, so it is behind the token like every page), and the pair "a
// change sends an event" / "no change sends nothing", which is the whole
// contract with the page's script.
func TestEvents(t *testing.T) {
	t.Run("no token is refused, with no stream", func(t *testing.T) {
		rg := newLiveRig(t, 0)
		srv := httptest.NewServer(rg.h)
		t.Cleanup(srv.Close)
		resp, err := http.Get(srv.URL + "/events")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); strings.Contains(ct, "event-stream") {
			t.Errorf("Content-Type = %q, want no event stream", ct)
		}
	})

	t.Run("headers and the retry line", func(t *testing.T) {
		rg := newLiveRig(t, 0)
		srv := httptest.NewServer(rg.h)
		t.Cleanup(srv.Close)
		st := openStream(t, srv)
		for k, want := range map[string]string{
			"Content-Type":      "text/event-stream",
			"Cache-Control":     "no-store",
			"X-Accel-Buffering": "no",
		} {
			if got := st.resp.Header.Get(k); got != want {
				t.Errorf("%s = %q, want %q", k, got, want)
			}
		}
		if before := st.until("retry: 3000"); len(before) != 0 {
			t.Errorf("lines before the retry line: %q", before)
		}
	})

	t.Run("a change sends an event", func(t *testing.T) {
		rg := newLiveRig(t, 0)
		srv := httptest.NewServer(rg.h)
		t.Cleanup(srv.Close)
		st := openStream(t, srv)
		st.until("retry: 3000")
		rg.syncPing(st)
		rg.addItem(rg.app, "abc234", "Which database?")
		rg.tick()
		if got := st.until(""); !reflect.DeepEqual(got, []string{"event: change", `data: {"project":"app","part":"inbox"}`}) {
			t.Errorf("event = %q", got)
		}
	})

	// Load-bearing: this is the wiring between the refresher and the reader.
	// The reading fails, so no file changes at all; each event here comes
	// from the refresher's generation alone, and without it an open usage
	// page would show Refreshing forever.
	t.Run("a usage refresh sends a usage event when it starts and when it fails", func(t *testing.T) {
		rg := newLiveRig(t, 0)
		srv := httptest.NewServer(rg.h)
		t.Cleanup(srv.Close)
		st := openStream(t, srv)
		st.until("retry: 3000")
		rg.syncPing(st)
		want := []string{"event: change", `data: {"project":"","part":"usage"}`}

		req := httptest.NewRequest(http.MethodPost, "/api/usage/refresh", nil)
		req.Header.Set("Authorization", "Bearer "+testToken)
		rec := httptest.NewRecorder()
		rg.h.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("POST /api/usage/refresh = %d", rec.Code)
		}
		if got := events(rg.syncPing(st)); !reflect.DeepEqual(got, want) {
			t.Errorf("on start the stream sent %q, want %q", got, want)
		}

		rg.release()
		rg.srv.refresher.wait()
		if got := events(rg.syncPing(st)); !reflect.DeepEqual(got, want) {
			t.Errorf("on failure the stream sent %q, want %q", got, want)
		}
		if got := events(rg.syncPing(st)); len(got) != 0 {
			t.Errorf("with nothing changed the stream sent %q", got)
		}
	})

	t.Run("no change sends nothing", func(t *testing.T) {
		rg := newLiveRig(t, 0)
		srv := httptest.NewServer(rg.h)
		t.Cleanup(srv.Close)
		st := openStream(t, srv)
		st.until("retry: 3000")
		// Two whole rounds: the first is the baseline, the second compares.
		for round := range 2 {
			if got := events(rg.syncPing(st)); len(got) != 0 {
				t.Errorf("round %d sent %q with nothing changed", round, got)
			}
		}
	})

	t.Run("a full queue drops only that browser", func(t *testing.T) {
		// Ten projects so one tick can send ten events: fewer than the
		// healthy stream's queue holds, so it is never at risk, while the
		// stuck one fills after two such ticks.
		rg := newLiveRig(t, 8)
		srv := httptest.NewServer(rg.h)
		t.Cleanup(srv.Close)
		healthy := openStream(t, srv)
		healthy.until("retry: 3000")

		stuck := newStuckWriter()
		req := httptest.NewRequest(http.MethodGet, "/events", nil)
		req.Header.Set("Authorization", "Bearer "+testToken)
		served := make(chan struct{})
		go func() {
			defer close(served)
			rg.h.ServeHTTP(stuck, req)
		}()
		select {
		case <-stuck.started:
		case <-time.After(10 * time.Second):
			t.Fatal("the stuck stream never started writing")
		}

		rg.syncPing(healthy) // baseline; one ping now waits in the stuck queue
		for burst := range 2 {
			for _, p := range rg.projects {
				rg.addItem(p.Root, "abc23"+string(rune('2'+burst)), "item")
			}
			rg.tick()
			for range rg.projects {
				healthy.until("")
			}
		}
		rg.addItem(rg.app, "def567", "after the drop")
		rg.tick()
		if got := healthy.until(""); !reflect.DeepEqual(got, []string{"event: change", `data: {"project":"app","part":"inbox"}`}) {
			t.Errorf("the healthy stream got %q after the drop", got)
		}

		// Let the stuck browser write again. A stream the hub still held
		// would wait for more; a dropped one drains its queue and ends.
		close(stuck.release)
		select {
		case <-served:
		case <-time.After(10 * time.Second):
			t.Fatal("the stuck stream's response did not end: it was not dropped")
		}
		// The queue held the baseline ping and fifteen of the first
		// twenty changes; nothing after the drop reached it.
		if n := strings.Count(stuck.body(), "event: change"); n != liveQueue-1 {
			t.Errorf("the stuck stream wrote %d events, want %d:\n%s", n, liveQueue-1, stuck.body())
		}
	})

	t.Run("the stream after the last one is refused", func(t *testing.T) {
		rg := newLiveRig(t, 0)
		srv := httptest.NewServer(rg.h)
		t.Cleanup(srv.Close)
		for range liveMaxStreams {
			openStream(t, srv).until("retry: 3000")
		}
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/events", nil)
		req.Header.Set("Authorization", "Bearer "+testToken)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); strings.Contains(ct, "event-stream") {
			t.Errorf("Content-Type = %q, want no event stream", ct)
		}
	})

	t.Run("no /api twin", func(t *testing.T) {
		rg := newLiveRig(t, 0)
		req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
		req.Header.Set("Authorization", "Bearer "+testToken)
		rec := httptest.NewRecorder()
		rg.h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("/api/events answered %d, want 404", rec.Code)
		}
	})
}

// stuckWriter is a browser that has stopped reading: its first write blocks
// until release is closed, as a write to a full socket does.
type stuckWriter struct {
	header  http.Header
	started chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	buf     strings.Builder
}

func newStuckWriter() *stuckWriter {
	return &stuckWriter{header: http.Header{}, started: make(chan struct{}), release: make(chan struct{})}
}

func (w *stuckWriter) Header() http.Header { return w.header }
func (w *stuckWriter) WriteHeader(int)     {}
func (w *stuckWriter) Flush()              {}

func (w *stuckWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	<-w.release
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *stuckWriter) body() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// TestFormatLive pins the wire form of each message.
func TestFormatLive(t *testing.T) {
	cases := []struct {
		name string
		msg  liveMsg
		want string
	}{
		{"change", liveMsg{change: change{"my app", partLedger}}, "event: change\ndata: {\"project\":\"my app\",\"part\":\"ledger\"}\n\n"},
		{"machine-wide", liveMsg{change: change{"", partUsage}}, "event: change\ndata: {\"project\":\"\",\"part\":\"usage\"}\n\n"},
		{"ping", liveMsg{ping: true}, ": ping\n\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatLive(tc.msg); got != tc.want {
				t.Errorf("formatLive = %q, want %q", got, tc.want)
			}
		})
	}
}
