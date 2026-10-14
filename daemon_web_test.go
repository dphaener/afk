package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/daemon"
	"github.com/dphaener/afk/internal/daemonconfig"
	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/decisionlog"
	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/host"
	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/session"
	"github.com/dphaener/afk/internal/state"
	"github.com/dphaener/afk/internal/web"
)

// pipeListener is a net.Listener with no port behind it: each connection is
// one end of an in-memory pipe. It is what lets the tests run a real
// http.Server (so the wiring in runDaemon and serveDaemonWeb is the code under
// test) without binding anything, which a sandbox may refuse and which could
// collide with whatever else the machine runs.
type pipeListener struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newPipeListener() *pipeListener {
	return &pipeListener{conns: make(chan net.Conn), closed: make(chan struct{})}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

// Close is safe to call twice; a stopping server and its caller both do.
func (l *pipeListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *pipeListener) Addr() net.Addr { return pipeAddr{} }

func (l *pipeListener) isClosed() bool {
	select {
	case <-l.closed:
		return true
	default:
		return false
	}
}

// dial hands the server one end of a new pipe and returns the other. It fails
// once the listener is closed, as a dial to a closed port does.
func (l *pipeListener) dial() (net.Conn, error) {
	client, server := net.Pipe()
	select {
	case l.conns <- server:
		return client, nil
	case <-l.closed:
		_ = client.Close()
		_ = server.Close()
		return nil, net.ErrClosed
	}
}

// request sends one GET over the listener and returns the status code, or an
// error when no connection could be made. token, when set, is sent as the
// bearer token.
func (l *pipeListener) request(path, token string) (int, error) {
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext:       func(context.Context, string, string) (net.Conn, error) { return l.dial() },
		},
	}
	req, err := http.NewRequest(http.MethodGet, "http://afk.test"+path, nil)
	if err != nil {
		return 0, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

// get is request for a page that must be up.
func (l *pipeListener) get(t *testing.T, path, token string) int {
	t.Helper()
	code, err := l.request(path, token)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return code
}

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }

// brokenListener fails every Accept with an error that is not temporary, the
// way a listener does when it is lost for good.
type brokenListener struct{ *pipeListener }

func (l *brokenListener) Accept() (net.Conn, error) {
	return nil, errors.New("accept: the socket is gone")
}

// lockedLog is a log a second goroutine may write to.
type lockedLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedLog) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(&l.buf, format+"\n", args...)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// webRig is one project with the web page set up the way runDaemon sets it
// up: the machine config is read from disk, the projects go through
// daemonProjects, and the handler is built from daemonWebDeps. Requests go
// straight to the handler; no server and no port.
type webRig struct {
	*daemonRig
	root    string
	handler http.Handler
	// list is the project list the page asks, as runDaemon's is: a test
	// changes it with list.set to stand in for a pass that read a new config.
	list *projectList
}

// newWebRig makes the rig. One dependency is replaced after daemonWebDeps
// built it: Sessions. The real one lists git worktrees, and a test must not
// run git over a temp folder to learn that it holds no sessions; what it
// would return is covered by TestWebSessionViews.
func newWebRig(t *testing.T) *webRig {
	t.Helper()
	r := newDaemonRig(t)
	root := r.project()
	r.writeConfig(fmt.Sprintf("[web]\ntoken = %q\n\n[[project]]\nname = \"demo\"\npath = %q\n", daemonTestWebToken, root))
	env := r.env()
	cfg, err := daemonconfig.Load(env.config)
	if err != nil {
		t.Fatal(err)
	}
	projects, _ := daemonProjects(cfg.Projects, env.gitCommonDir)
	list := &projectList{}
	list.set(projects)
	deps := daemonWebDeps(list.get, r.stateDir(), env.state, env.now)
	deps.Sessions = func(string) []web.SessionView { return nil }
	h, err := web.New(web.Config{Token: cfg.Web.Token, PublicURL: cfg.Web.PublicURL}, deps)
	if err != nil {
		t.Fatal(err)
	}
	return &webRig{daemonRig: r, root: root, handler: h, list: list}
}

// get sends a GET to the handler with the token.
func (w *webRig) get(path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+daemonTestWebToken)
	rec := httptest.NewRecorder()
	w.handler.ServeHTTP(rec, req)
	return rec
}

// post sends a form POST to the handler. token, when set, is the bearer
// token.
func (w *webRig) post(path, token, form string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	w.handler.ServeHTTP(rec, req)
	return rec
}

// decisionKinds returns the kinds in a project's decision log, in order.
func decisionKinds(t *testing.T, root string) []string {
	t.Helper()
	entries, err := decisionlog.Read(decisionlog.Path(root))
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range entries {
		if e.ChosenBy != decisionlog.ChosenByOwner {
			t.Errorf("decision %q is chosen by %q, want the owner", e.Kind, e.ChosenBy)
		}
		kinds = append(kinds, e.Kind)
	}
	return kinds
}

// TestDaemonWebAnswerUnparksTheTask is the acceptance test for the page's one
// real job: an answer given on the page gets a parked task moving again.
//
// Each row starts from the same project: T1.1 is parked on one blocking inbox
// item. The row sends one POST to the page's answer address, then runs one
// daemon pass (`afk daemon --once`, with the rig's fake loop deps), and reads
// the item and the ledger back from disk.
//
// Load-bearing rows:
//   - "answered with the token": the item is answered via "web" and, after
//     one pass, the ledger says running. The handler wrote nothing but the
//     item; it is the loop that un-parks, from the file, with no memory shared
//     between the two.
//   - "no token" and "a wrong token" are the same POST refused. The item
//     stays open and the task stays parked, which is also what shows the
//     first row is the answer's doing and not something any pass would do.
func TestDaemonWebAnswerUnparksTheTask(t *testing.T) {
	tests := []struct {
		name       string
		token      string
		form       string
		wantCode   int
		wantVia    string
		wantStatus ledger.Status
		wantOnce   string
	}{
		{name: "answered with the token", token: daemonTestWebToken, form: "answer=Sidekiq",
			wantCode: http.StatusOK, wantVia: inbox.ViaWeb, wantStatus: ledger.Running,
			wantOnce: "demo: a parked task is running again (T1.1, session t1-1)"},
		{name: "no token", form: "answer=Sidekiq",
			wantCode: http.StatusUnauthorized, wantStatus: ledger.Parked,
			wantOnce: "waiting on owner: T1.1 parked; answer inbox"},
		{name: "a wrong token", token: "tok-0000000000000000", form: "answer=Sidekiq",
			wantCode: http.StatusUnauthorized, wantStatus: ledger.Parked,
			wantOnce: "waiting on owner: T1.1 parked; answer inbox"},
		{name: "the token, but an empty answer", token: daemonTestWebToken, form: "answer=",
			wantCode: http.StatusBadRequest, wantStatus: ledger.Parked,
			wantOnce: "waiting on owner: T1.1 parked; answer inbox"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWebRig(t)
			store := inbox.Store{Dir: inbox.Dir(w.root), Now: func() time.Time { return daemonTestNow }}
			item, err := store.Add(inbox.NewItem{Task: "T1.1", Session: "t1-1", Kind: "question", Blocking: true, Title: "Which queue?"})
			if err != nil {
				t.Fatal(err)
			}
			if err := ledger.Update(w.root, func(l *ledger.Ledger) error {
				l.Tasks = append(l.Tasks, ledger.Entry{ID: "T1.1", Status: ledger.Parked, Session: "t1-1", ParkedOn: []string{item.ID}})
				return nil
			}); err != nil {
				t.Fatal(err)
			}

			rec := w.post("/api/p/demo/inbox/"+item.ID+"/answer", tt.token, tt.form)
			t.Logf("POST answer: %d %s", rec.Code, strings.TrimSpace(rec.Body.String()))
			if rec.Code != tt.wantCode {
				t.Fatalf("the answer POST got %d, want %d", rec.Code, tt.wantCode)
			}
			if strings.Contains(rec.Body.String(), daemonTestWebToken) {
				t.Error("the answer holds the web token")
			}
			got := readInboxItem(t, w.root, item.ID)
			if got.AnsweredVia != tt.wantVia {
				t.Errorf("answered_via = %q, want %q", got.AnsweredVia, tt.wantVia)
			}
			if answered := !got.Open(); answered != (tt.wantVia != "") {
				t.Errorf("item answered = %v, want %v", answered, tt.wantVia != "")
			}
			// The POST alone must leave the ledger as it was: un-parking is
			// the loop's, on its next pass.
			if l, err := ledger.Load(w.root); err != nil || l.Status("T1.1") != ledger.Parked {
				t.Fatalf("after the POST and before any pass, T1.1 is %q (err %v), want parked", l.Status("T1.1"), err)
			}

			var so, se bytes.Buffer
			if code := runDaemon([]string{"--once"}, &so, &se, w.env()); code != 0 {
				t.Fatalf("the pass exited %d: %s", code, se.String())
			}
			t.Logf("one pass: %s", strings.TrimSpace(so.String()))
			if !strings.Contains(so.String(), tt.wantOnce) {
				t.Errorf("the pass printed %q, want it to hold %q", so.String(), tt.wantOnce)
			}
			l, err := ledger.Load(w.root)
			if err != nil {
				t.Fatal(err)
			}
			e, _ := l.Get("T1.1")
			t.Logf("ledger after the pass: T1.1 is %s", e.Status)
			if e.Status != tt.wantStatus {
				t.Errorf("after one pass T1.1 is %q, want %q", e.Status, tt.wantStatus)
			}
			if tt.wantStatus == ledger.Running && len(e.ParkedOn) != 0 {
				t.Errorf("T1.1 is running but still lists parked_on %v", e.ParkedOn)
			}
			if len(w.launches) != 0 {
				t.Errorf("the pass launched %d session(s), want none: the task's session is still there", len(w.launches))
			}
		})
	}
}

// TestDaemonWebPauseAndResume presses the page's two buttons through the real
// wiring and reads the files `afk pause` and `afk resume` write.
//
// The rows are steps on one project, in order, because the second half of
// each is "and the state from the step before is what it changed".
//
// Load-bearing: "pause without the token" (a refused button must write
// nothing at all: no flag, no decision line) and "pause" and "resume" (the
// flag appears and goes, and each press is one line in the decision log with
// the owner as the one who chose, exactly as the commands leave it).
func TestDaemonWebPauseAndResume(t *testing.T) {
	w := newWebRig(t)
	steps := []struct {
		name       string
		path       string
		token      string
		wantCode   int
		wantPaused bool
		wantKinds  []string
	}{
		{name: "pause without the token", path: "/api/p/demo/pause", wantCode: http.StatusUnauthorized},
		{name: "pause", path: "/api/p/demo/pause", token: daemonTestWebToken,
			wantCode: http.StatusOK, wantPaused: true, wantKinds: []string{"pause"}},
		{name: "resume without the token", path: "/api/p/demo/resume",
			wantCode: http.StatusUnauthorized, wantPaused: true, wantKinds: []string{"pause"}},
		{name: "resume", path: "/api/p/demo/resume", token: daemonTestWebToken,
			wantCode: http.StatusOK, wantKinds: []string{"pause", "resume"}},
		{name: "resume with nothing to resume", path: "/api/p/demo/resume", token: daemonTestWebToken,
			wantCode: http.StatusOK, wantKinds: []string{"pause", "resume"}},
		{name: "a project the page does not serve", path: "/api/p/other/pause", token: daemonTestWebToken,
			wantCode: http.StatusNotFound, wantKinds: []string{"pause", "resume"}},
	}
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			rec := w.post(st.path, st.token, "")
			if rec.Code != st.wantCode {
				t.Fatalf("POST %s got %d, want %d: %s", st.path, rec.Code, st.wantCode, rec.Body.String())
			}
			if got := daemonstate.Paused(w.root); got != st.wantPaused {
				t.Errorf("paused = %v, want %v", got, st.wantPaused)
			}
			if _, err := os.Stat(daemonstate.PausePath(w.root)); (err == nil) != st.wantPaused {
				t.Errorf("the pause flag file exists = %v, want %v", err == nil, st.wantPaused)
			}
			if got := decisionKinds(t, w.root); !reflect.DeepEqual(got, st.wantKinds) {
				t.Errorf("decision log kinds = %q, want %q", got, st.wantKinds)
			}
			if strings.Contains(rec.Body.String(), daemonTestWebToken) {
				t.Error("the answer holds the web token")
			}
		})
	}
}

// TestServeDaemonWeb runs the server over a listener with no port.
//
// Load-bearing: "stop closes the page" (once stop returns the listener is
// closed and a new connection is refused; a listener left open keeps the
// port from the next daemon) and "a listener that breaks" (the server ending
// by itself is one log line and a stop that still returns; nothing in it can
// reach the build loop, which is not even here).
func TestServeDaemonWeb(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })

	t.Run("serves, and stop closes the page", func(t *testing.T) {
		ln := newPipeListener()
		var log lockedLog
		stop := serveDaemonWeb(ln, ok, log.logf)
		if code := ln.get(t, "/", ""); code != http.StatusOK {
			t.Errorf("the page answered %d, want 200", code)
		}
		stop()
		if !ln.isClosed() {
			t.Error("the listener is still open after stop")
		}
		if _, err := ln.request("/", ""); err == nil {
			t.Error("the page still answers after stop")
		}
		if log.String() != "" {
			t.Errorf("a clean stop logged %q, want nothing", log.String())
		}
	})

	t.Run("stop right after the start", func(t *testing.T) {
		// No request and no wait: stop may run before the serving goroutine
		// has started, and must still close the listener and return.
		ln := newPipeListener()
		var log lockedLog
		serveDaemonWeb(ln, ok, log.logf)()
		if !ln.isClosed() {
			t.Error("the listener is still open after stop")
		}
		if log.String() != "" {
			t.Errorf("a clean stop logged %q, want nothing", log.String())
		}
	})

	t.Run("stop ends an open live stream at once", func(t *testing.T) {
		// The real page, so the stream is the real one: a request that never
		// ends by itself. Without the cancelled base context, Shutdown would
		// wait its whole webShutdownLimit for it.
		w := newWebRig(t)
		ln := newPipeListener()
		var log lockedLog
		stop := serveDaemonWeb(ln, w.handler, log.logf)
		client := &http.Client{Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext:       func(context.Context, string, string) (net.Conn, error) { return ln.dial() },
		}}
		req, err := http.NewRequest(http.MethodGet, "http://afk.test/events", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+daemonTestWebToken)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("/events answered %d, want 200", resp.StatusCode)
		}
		// The first line proves the handler is inside its loop, holding
		// the request open.
		body := bufio.NewReader(resp.Body)
		line, err := body.ReadString('\n')
		if err != nil || line != "retry: 3000\n" {
			t.Fatalf("first line = %q, %v; want the retry line", line, err)
		}
		// Keep reading, as a browser does. A pipe has no buffer, so a
		// client that stopped reading would hold the server's last write
		// (the end of the response) and time the test, not the server.
		ended := make(chan struct{})
		go func() {
			defer close(ended)
			_, _ = io.Copy(io.Discard, body)
		}()

		start := time.Now()
		stop()
		if took := time.Since(start); took > webShutdownLimit/2 {
			t.Errorf("stop took %v with a stream open, want well under %v", took, webShutdownLimit)
		}
		select {
		case <-ended:
		case <-time.After(10 * time.Second):
			t.Error("the stream was still open after stop")
		}
		if log.String() != "" {
			t.Errorf("a clean stop logged %q, want nothing", log.String())
		}
	})

	t.Run("a listener that breaks is a log line, and stop still returns", func(t *testing.T) {
		ln := &brokenListener{pipeListener: newPipeListener()}
		var log lockedLog
		logged := make(chan struct{}, 1)
		stop := serveDaemonWeb(ln, ok, func(format string, args ...any) {
			log.logf(format, args...)
			logged <- struct{}{}
		})
		// The line is waited for before stop is called. A stop that got in
		// first would make the failed Accept look like an ordinary shutdown,
		// which is silent on purpose.
		select {
		case <-logged:
		case <-time.After(10 * time.Second):
			t.Fatal("the server lost its listener and logged nothing")
		}
		stop()
		for _, want := range []string{"web page: it stopped answering", "the socket is gone", "The build carries on without it"} {
			if !strings.Contains(log.String(), want) {
				t.Errorf("log %q lacks %q", log.String(), want)
			}
		}
	})
}

// TestWebSessionViews pins what the page is told about each session.
//
// Load-bearing: the one-off row. A session made with `afk new --scratch` has
// no status.json, so discovery reports an empty phase that is not stale; it
// is passed on as stale so the page says "unknown" and not a blank.
func TestWebSessionViews(t *testing.T) {
	tests := []struct {
		name string
		in   discover.SessionState
		want web.SessionView
	}{
		{name: "a working session",
			in:   discover.SessionState{Name: "t1-1", Phase: state.PhaseExecuting, HostRunning: true},
			want: web.SessionView{Name: "t1-1", Phase: "executing", HostRunning: true}},
		{name: "waiting at its terminal",
			in:   discover.SessionState{Name: "t1-1", Phase: state.PhaseReviewing, AwaitingInput: true, HostRunning: true},
			want: web.SessionView{Name: "t1-1", Phase: "reviewing", AwaitingInput: true, HostRunning: true}},
		{name: "a status file that cannot be read",
			in:   discover.SessionState{Name: "t1-1", StatusStale: true, Kind: session.KindStaleStatus},
			want: web.SessionView{Name: "t1-1", Stale: true}},
		{name: "a one-off session has no phase",
			in:   discover.SessionState{Name: "scratch", Scratch: true, HostRunning: true},
			want: web.SessionView{Name: "scratch", Stale: true, HostRunning: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := webSessionViews([]discover.SessionState{tt.in})
			if len(got) != 1 || got[0] != tt.want {
				t.Errorf("views = %+v, want exactly %+v", got, tt.want)
			}
		})
	}
	if got := webSessionViews(nil); len(got) != 0 {
		t.Errorf("no sessions gave %+v, want none", got)
	}
}

// TestWebPauseError pins the sentence the page shows when a button fails.
//
// Load-bearing: the three log rows. There the pause or resume itself went
// through, and the sentence has to say so, or the owner presses the button
// again for something that is already done.
func TestWebPauseError(t *testing.T) {
	cause := errors.New("open x: permission denied")
	tests := []struct {
		name string
		err  error
		want []string
	}{
		{name: "no error"},
		{name: "the flag could not be written", err: &pauseError{stage: stagePauseFlag, err: cause},
			want: []string{"permission denied", "Check that afk can write to", "/p/.afk/build"}},
		{name: "the stop marker could not be removed", err: &pauseError{stage: stageStopMarker, err: cause},
			want: []string{"permission denied", "Check that afk can write to", "/p/.afk/build"}},
		{name: "paused, log failed", err: &pauseError{stage: stageLogPause, err: cause},
			want: []string{"the project is paused, but the decision log could not be written", "permission denied"}},
		{name: "resumed, log failed", err: &pauseError{stage: stageLogResume, err: cause},
			want: []string{"the project is resumed, but the decision log could not be written", "permission denied"}},
		{name: "stop cleared, log failed", err: &pauseError{stage: stageLogStopCleared, err: cause},
			want: []string{"the stopped line is cleared, but the decision log could not be written", "permission denied"}},
		{name: "some other error is passed on", err: cause, want: []string{"open x: permission denied"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := webPauseError("/p", tt.err)
			if tt.err == nil {
				if got != nil {
					t.Fatalf("got %v, want no error", got)
				}
				return
			}
			if got == nil {
				t.Fatal("got no error")
			}
			if !errors.Is(got, cause) {
				t.Errorf("%v does not wrap its cause", got)
			}
			for _, w := range tt.want {
				if !strings.Contains(got.Error(), w) {
					t.Errorf("%q lacks %q", got.Error(), w)
				}
			}
		})
	}
}

// TestWebPublicURL pins when a notification may carry a link.
//
// Load-bearing: "an address but no token". With no token the page is off, so
// a link would open nothing.
func TestWebPublicURL(t *testing.T) {
	tests := []struct {
		name string
		in   daemonconfig.Web
		want string
	}{
		{name: "nothing set", in: daemonconfig.Web{Bind: daemonconfig.DefaultWebBind}},
		{name: "a token and an address", in: daemonconfig.Web{Token: daemonTestWebToken, PublicURL: "https://afk.example"}, want: "https://afk.example"},
		{name: "a token but no address", in: daemonconfig.Web{Token: daemonTestWebToken}},
		{name: "an address but no token", in: daemonconfig.Web{PublicURL: "https://afk.example"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := webPublicURL(tt.in); got != tt.want {
				t.Errorf("webPublicURL = %q, want %q", got, tt.want)
			}
		})
	}
}

// webTestOverview and webTestPlan are the keys of the page's JSON these tests
// read; the full shapes are internal/web's to test.
type webTestOverview struct {
	Projects []struct {
		Name string `json:"name"`
	} `json:"projects"`
	Reviews []struct {
		Name string `json:"name"`
	} `json:"reviews"`
}

type webTestPlan struct {
	Path               string `json:"path"`
	WaitingForApproval bool   `json:"waiting_for_approval"`
	Plan               string `json:"plan"`
	Problem            string `json:"problem"`
}

// TestDaemonWebPlans asks the page, wired as runDaemon wires it, for plans and
// for the list of them. The machine has one configured project (demo) and
// three plans on the review list: one for a folder nobody configured (the
// plan waiting for approval), one for demo's own folder (what is left when
// approve added the project and had not yet taken the entry off), and one for
// a folder with no plan file.
//
// Load-bearing rows:
//   - "a plan waiting for approval" is the reason the review list exists: the
//     page serves a plan for a folder config.toml does not name, read from the
//     default path because the folder has no afk.toml yet.
//   - "a configured project, plan named by afk.toml" pins that the plan is
//     found through the project's own config and not assumed to be
//     docs/PLAN.md.
//   - "the overview lists a configured project once" is the double listing:
//     demo is on the review list too and must show as a project only.
//   - "a project added after the page started" changes the list the way a
//     pass of the loop does and asks again with the same handler. A page built
//     over a list copied at start-up answers 404 here.
func TestDaemonWebPlans(t *testing.T) {
	w := newWebRig(t)
	// demo keeps its plan somewhere of its own.
	const movedPlan = "#### T9.1 — Moved\n\n- **Depends on:** none\n"
	writeBuildFile(t, w.root, "afk.toml", "plan = \"notes/BUILD.md\"\n")
	writeBuildFile(t, w.root, "notes/BUILD.md", movedPlan)

	base := t.TempDir()
	waiting := filepath.Join(base, "recipes")
	writeBuildFile(t, waiting, "docs/PLAN.md", daemonTestPlan)
	empty := filepath.Join(base, "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	later := filepath.Join(base, "later")
	writeBuildFile(t, later, "docs/PLAN.md", daemonTestPlan)
	at := daemonTestNow.Format(time.RFC3339)
	for _, root := range []string{waiting, w.root, empty} {
		if err := daemonstate.AddReview(w.stateDir(), daemonstate.Review{Root: root, At: at}); err != nil {
			t.Fatal(err)
		}
	}
	demoReview := filepath.Base(w.root)

	plan := func(t *testing.T, path string, wantCode int) webTestPlan {
		t.Helper()
		rec := w.get(path)
		if rec.Code != wantCode {
			t.Fatalf("GET %s got %d, want %d: %s", path, rec.Code, wantCode, rec.Body.String())
		}
		var v webTestPlan
		if wantCode == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
				t.Fatalf("GET %s: %v in %s", path, err, rec.Body.String())
			}
		}
		return v
	}
	names := func(t *testing.T) (projects, reviews []string) {
		t.Helper()
		rec := w.get("/api/")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/ got %d: %s", rec.Code, rec.Body.String())
		}
		var v webTestOverview
		if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		for _, p := range v.Projects {
			projects = append(projects, p.Name)
		}
		for _, r := range v.Reviews {
			reviews = append(reviews, r.Name)
		}
		return projects, reviews
	}

	t.Run("a plan waiting for approval", func(t *testing.T) {
		v := plan(t, "/api/p/recipes/plan", http.StatusOK)
		if !v.WaitingForApproval || v.Problem != "" || v.Plan != daemonTestPlan {
			t.Errorf("plan view = %+v, want the waiting plan with no problem", v)
		}
		if want := filepath.Join(waiting, "docs", "PLAN.md"); v.Path != want {
			t.Errorf("path = %q, want %q", v.Path, want)
		}
		if rec := w.get("/p/recipes/plan"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "T1.1") {
			t.Errorf("the page for the waiting plan got %d and does not show the plan", rec.Code)
		}
	})
	t.Run("a configured project, plan named by afk.toml", func(t *testing.T) {
		v := plan(t, "/api/p/demo/plan", http.StatusOK)
		if v.WaitingForApproval || v.Problem != "" || v.Plan != movedPlan {
			t.Errorf("plan view = %+v, want demo's own plan, not waiting, with no problem", v)
		}
		if want := filepath.Join(w.root, "notes", "BUILD.md"); v.Path != want {
			t.Errorf("path = %q, want %q", v.Path, want)
		}
	})
	t.Run("a waiting plan whose file is missing says which file", func(t *testing.T) {
		v := plan(t, "/api/p/empty/plan", http.StatusOK)
		want := filepath.Join(empty, "docs", "PLAN.md")
		if v.Path != want || !strings.Contains(v.Problem, want) || v.Plan != "" {
			t.Errorf("plan view = %+v, want a problem naming %s", v, want)
		}
	})
	t.Run("the overview lists a configured project once", func(t *testing.T) {
		projects, reviews := names(t)
		if !reflect.DeepEqual(projects, []string{"demo"}) || !reflect.DeepEqual(reviews, []string{"empty", "recipes"}) {
			t.Errorf("projects = %q, reviews = %q, want demo and then empty, recipes", projects, reviews)
		}
		// The entry is there under the folder's name; it is the configured
		// project's root that keeps it off the page.
		if demoReview != "demo" {
			plan(t, "/api/p/"+demoReview+"/plan", http.StatusNotFound)
		}
	})
	t.Run("a name nobody has", func(t *testing.T) {
		plan(t, "/api/p/later/plan", http.StatusNotFound)
	})
	t.Run("a project added after the page started", func(t *testing.T) {
		w.list.set([]daemon.Project{{Name: "demo", Root: w.root}, {Name: "later", Root: later}, {Name: "recipes", Root: waiting}})
		if v := plan(t, "/api/p/later/plan", http.StatusOK); v.WaitingForApproval || v.Plan != daemonTestPlan {
			t.Errorf("plan view = %+v, want the new project's plan, not waiting", v)
		}
		// recipes was approved: it is a project now and no longer waiting.
		if v := plan(t, "/api/p/recipes/plan", http.StatusOK); v.WaitingForApproval {
			t.Error("recipes is a configured project and still shown as waiting for approval")
		}
		projects, reviews := names(t)
		if !reflect.DeepEqual(projects, []string{"demo", "later", "recipes"}) || !reflect.DeepEqual(reviews, []string{"empty"}) {
			t.Errorf("projects = %q, reviews = %q, want demo, later, recipes and then empty", projects, reviews)
		}
	})
	t.Run("a project taken out again", func(t *testing.T) {
		w.list.set(nil)
		plan(t, "/api/p/demo/plan", http.StatusNotFound)
		projects, _ := names(t)
		if len(projects) != 0 {
			t.Errorf("projects = %q, want none", projects)
		}
	})
}

// TestWebReviews covers which plans waiting for approval the page is handed.
//
// Load-bearing: "the same folder written another way". Approve and the review
// list both clean the path they store, but the machine config keeps a path as
// the owner typed it, so the two are compared cleaned; compared as written,
// an approved project would stay listed as waiting.
func TestWebReviews(t *testing.T) {
	tests := []struct {
		name       string
		reviews    []daemonstate.Review
		configured []web.Project
		want       []web.Project
	}{
		{name: "nothing waiting", want: []web.Project{}},
		{
			name:    "one waiting, nothing configured",
			reviews: []daemonstate.Review{{Name: "app", Root: "/work/app"}},
			want:    []web.Project{{Name: "app", Root: "/work/app"}},
		},
		{
			name:       "a configured project's entry is left out",
			reviews:    []daemonstate.Review{{Name: "app", Root: "/work/app"}, {Name: "blog", Root: "/work/blog"}},
			configured: []web.Project{{Name: "mine", Root: "/work/app"}},
			want:       []web.Project{{Name: "blog", Root: "/work/blog"}},
		},
		{
			name:       "the same folder written another way",
			reviews:    []daemonstate.Review{{Name: "app", Root: "/work/app"}},
			configured: []web.Project{{Name: "app", Root: "/work/x/../app/"}},
			want:       []web.Project{},
		},
		{
			name:       "the same name in another folder is this function's to keep",
			reviews:    []daemonstate.Review{{Name: "app", Root: "/other/app"}},
			configured: []web.Project{{Name: "app", Root: "/work/app"}},
			want:       []web.Project{{Name: "app", Root: "/other/app"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := webReviews(tt.reviews, tt.configured); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("webReviews = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestWebReadPlan covers where the page looks for a project's plan.
//
// Load-bearing: "afk.toml is damaged". The plan path cannot be known then, so
// the answer is the error and no path; reading docs/PLAN.md anyway would show
// a plan that may not be the one the project builds from.
func TestWebReadPlan(t *testing.T) {
	tests := []struct {
		name     string
		files    map[string]string
		wantPath string // under the project folder; "" for none
		wantData string
		wantErr  string
	}{
		{
			name:     "no afk.toml: the default path",
			files:    map[string]string{"docs/PLAN.md": "the plan\n"},
			wantPath: "docs/PLAN.md",
			wantData: "the plan\n",
		},
		{
			name:     "afk.toml names the plan",
			files:    map[string]string{"afk.toml": "plan = \"PLAN.md\"\n", "PLAN.md": "mine\n", "docs/PLAN.md": "not this one\n"},
			wantPath: "PLAN.md",
			wantData: "mine\n",
		},
		{
			name:     "the plan is not there",
			files:    map[string]string{"afk.toml": "plan = \"PLAN.md\"\n"},
			wantPath: "PLAN.md",
			wantErr:  "PLAN.md",
		},
		{
			name:    "afk.toml is damaged",
			files:   map[string]string{"afk.toml": "plan = [not a path\n", "docs/PLAN.md": "the plan\n"},
			wantErr: "afk.toml",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for rel, content := range tt.files {
				writeBuildFile(t, root, rel, content)
			}
			path, data, err := webReadPlan(root)
			wantPath := ""
			if tt.wantPath != "" {
				wantPath = filepath.Join(root, filepath.FromSlash(tt.wantPath))
			}
			if path != wantPath || string(data) != tt.wantData {
				t.Errorf("webReadPlan = %q, %q; want %q, %q", path, data, wantPath, tt.wantData)
			}
			if (err != nil) != (tt.wantErr != "") || (err != nil && !strings.Contains(err.Error(), tt.wantErr)) {
				t.Errorf("error = %v, want one naming %q", err, tt.wantErr)
			}
		})
	}
}

// TestProjectList pins that the list is shared as copies only: what the loop
// sets and what the page gets are never the same slice, so neither can change
// the other's.
func TestProjectList(t *testing.T) {
	l := &projectList{}
	if got := l.get(); len(got) != 0 {
		t.Fatalf("a new list holds %v", got)
	}
	given := []daemon.Project{{Name: "a", Root: "/a"}, {Name: "b", Root: "/b"}}
	l.set(given)
	given[0].Name = "changed by the loop"
	got := l.get()
	got[1].Name = "changed by the page"
	want := []daemon.Project{{Name: "a", Root: "/a"}, {Name: "b", Root: "/b"}}
	if again := l.get(); !reflect.DeepEqual(again, want) {
		t.Errorf("the list is %v after both sides changed their copies, want %v", again, want)
	}
}

// TestDaemonWebPlanChat checks the real wiring of the planning chat: with no
// planning host, or a host whose process is gone, the chat is "none".
//
// Load-bearing row: "dead pid". A host.json is left behind when a planning
// session ends, so a transcript must not be read (or reported as a problem)
// for it; "none" is the only right answer.
func TestDaemonWebPlanChat(t *testing.T) {
	// A pid that is certainly dead: a child that has been run and waited for.
	child := exec.Command("true")
	if err := child.Run(); err != nil {
		t.Fatal(err)
	}
	deadPID := child.Process.Pid

	tests := []struct {
		name string
		meta *host.Meta
	}{
		{name: "no host.json"},
		{name: "dead pid", meta: &host.Meta{PID: deadPID, StartedAt: "2026-10-14T09:00:00Z"}},
		{name: "dead pid and unreadable start", meta: &host.Meta{PID: deadPID, StartedAt: "yesterday"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := newWebRig(t)
			if tc.meta != nil {
				if err := host.WriteMeta(w.root, session.PlanSessionName, *tc.meta); err != nil {
					t.Fatal(err)
				}
			}
			rec := w.get("/api/p/demo/plan/chat")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200\n%s", rec.Code, rec.Body.String())
			}
			var got struct {
				State    string `json:"state"`
				Messages []any  `json:"messages"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.State != "none" || got.Messages == nil || len(got.Messages) != 0 {
				t.Errorf("chat = %s, want state none and an empty list", rec.Body.String())
			}
		})
	}
}
