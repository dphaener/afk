package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// watchRig is the rig with a fake Deps.WatchScreen. The fake counts calls so
// a test can tell a read from a reuse.
func watchRig(t *testing.T, read func(ctx context.Context, root, session string, bound int64) (WatchReading, error)) *rig {
	t.Helper()
	rg := newRig(t, Config{})
	rg.srv.d.WatchScreen = read
	return rg
}

func rexReading(ctx context.Context, root, session string, bound int64) (WatchReading, error) {
	return WatchReading{Total: 20, HTML: []byte(`<pre>agent says "hi"</pre>`)}, nil
}

func get(rg *rig, target string) (int, string, http.Header) {
	rec := authReq{target: target, cookie: goodCookie()}.send(rg.h)
	return rec.Code, rec.Body.String(), rec.Header()
}

// TestWatchPage: the first-frame row is load-bearing (the page must hold the
// frame the first read returned, escaped into srcdoc, with the sandbox and
// its CSP line), and so are the not-Rex and missing rows (a plain line, and
// a 404 that never calls Rex).
func TestWatchPage(t *testing.T) {
	tests := []struct {
		name      string
		target    string
		read      func(ctx context.Context, root, session string, bound int64) (WatchReading, error)
		want      int
		contains  []string
		absent    []string
		wantCalls int
	}{
		{name: "rex session", target: "/p/app/sessions/t1-2/watch", read: rexReading, want: 200,
			contains: []string{`id="agent-watch"`, `data-project="app"`, `data-session="t1-2"`, `sandbox=""`, `title="Agent screen for t1-2"`,
				`srcdoc="&lt;meta http-equiv=&#34;Content-Security-Policy&#34;`, `agent says &#34;hi&#34;`, `--vt-palette-1:`, "/p/app/sessions/t1-2", "watch.js"},
			wantCalls: 1},
		{name: "not rex", target: "/p/app/sessions/t1-2/watch", want: 200,
			read: func(context.Context, string, string, int64) (WatchReading, error) {
				return WatchReading{}, fmt.Errorf("%w: x", ErrWatchNotRex)
			},
			contains:  []string{"This session does not run in Rex, so there is no screen to watch."},
			absent:    []string{`id="agent-watch"`, "<iframe"},
			wantCalls: 1},
		{name: "gone", target: "/p/app/sessions/t1-2/watch", want: 200,
			read: func(context.Context, string, string, int64) (WatchReading, error) {
				return WatchReading{}, fmt.Errorf("%w: x", ErrWatchGone)
			},
			contains: []string{"its Rex block has closed"}, absent: []string{"<iframe"}, wantCalls: 1},
		{name: "read error keeps the frame", target: "/p/app/sessions/t1-2/watch", want: 200,
			read: func(context.Context, string, string, int64) (WatchReading, error) {
				return WatchReading{}, errors.New("boom")
			},
			contains: []string{"Reload to see the latest", "<iframe"}, wantCalls: 1},
		{name: "no seam", target: "/p/app/sessions/t1-2/watch", want: 200,
			contains: []string{"Reload to see the latest"}, absent: []string{"<iframe"}},
		{name: "missing session", target: "/p/app/sessions/nope/watch", read: rexReading, want: 404,
			contains: []string{"There is no session with that name"}},
		{name: "bad name", target: "/p/app/sessions/..%2fx/watch", read: rexReading, want: 404},
		{name: "missing project", target: "/p/nope/sessions/t1-2/watch", read: rexReading, want: 404,
			contains: []string{"There is no project with that name"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			var read func(ctx context.Context, root, session string, bound int64) (WatchReading, error)
			if tc.read != nil {
				read = func(ctx context.Context, root, session string, bound int64) (WatchReading, error) {
					calls++
					return tc.read(ctx, root, session, bound)
				}
			}
			rg := watchRig(t, read)
			code, body, _ := get(rg, tc.target)
			if code != tc.want {
				t.Fatalf("status = %d, want %d\n%s", code, tc.want, body)
			}
			for _, s := range tc.contains {
				if !strings.Contains(body, s) {
					t.Errorf("page lacks %q\n%s", s, body)
				}
			}
			for _, s := range tc.absent {
				if strings.Contains(body, s) {
					t.Errorf("page has %q", s)
				}
			}
			if calls != tc.wantCalls {
				t.Errorf("WatchScreen called %d times, want %d", calls, tc.wantCalls)
			}
		})
	}
}

// TestWatchPageLease: loading the page is a watcher, so the live reader has
// someone to read for. Without the touch the first change would wait for the
// first ping.
func TestWatchPageLease(t *testing.T) {
	rg := watchRig(t, rexReading)
	if n := rg.srv.watch.Count(watchKey("app", "t1-2"), testNow); n != 0 {
		t.Fatalf("leases before = %d", n)
	}
	if code, _, _ := get(rg, "/p/app/sessions/t1-2/watch"); code != 200 {
		t.Fatalf("status = %d", code)
	}
	if n := rg.srv.watch.Count(watchKey("app", "t1-2"), testNow); n != 1 {
		t.Errorf("leases after = %d, want 1", n)
	}
}

// TestWatchScreen: the CSP row is load-bearing (the fragment is agent text),
// as is the slow header row (watch.js spaces its fetches by it) and the
// gone row (410 and a sentence, never an empty 200 that blanks the frame).
func TestWatchScreen(t *testing.T) {
	tests := []struct {
		name   string
		target string
		read   func(ctx context.Context, root, session string, bound int64) (WatchReading, error)
		want   int
		slow   string
		body   string
	}{
		{name: "fast", target: "/p/app/sessions/t1-2/watch/screen", read: rexReading, want: 200, body: "agent says"},
		{name: "slow", target: "/p/app/sessions/t1-2/watch/screen", want: 200, slow: "1", body: "tail",
			read: func(context.Context, string, string, int64) (WatchReading, error) {
				return WatchReading{Total: 999999, HTML: []byte("<pre>tail</pre>"), Slow: true}, nil
			}},
		{name: "gone", target: "/p/app/sessions/t1-2/watch/screen", want: 410, body: "its Rex block has closed",
			read: func(context.Context, string, string, int64) (WatchReading, error) {
				return WatchReading{Slow: true}, fmt.Errorf("%w: x", ErrWatchGone)
			}},
		{name: "not rex", target: "/p/app/sessions/t1-2/watch/screen", want: 404, body: "does not run in Rex",
			read: func(context.Context, string, string, int64) (WatchReading, error) {
				return WatchReading{}, fmt.Errorf("%w: x", ErrWatchNotRex)
			}},
		{name: "read error", target: "/p/app/sessions/t1-2/watch/screen", want: 502,
			read: func(context.Context, string, string, int64) (WatchReading, error) {
				return WatchReading{}, errors.New("boom")
			}},
		{name: "no seam", target: "/p/app/sessions/t1-2/watch/screen", want: 503},
		{name: "missing session", target: "/p/app/sessions/nope/watch/screen", read: rexReading, want: 404},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := watchRig(t, tc.read)
			code, body, h := get(rg, tc.target)
			if code != tc.want {
				t.Fatalf("status = %d, want %d\n%s", code, tc.want, body)
			}
			if !strings.Contains(body, tc.body) {
				t.Errorf("body %q lacks %q", body, tc.body)
			}
			if code != 200 {
				return
			}
			// Load-bearing: Rex colours are var(--vt-palette-N); without the
			// definitions in the fragment the live screen has no colour.
			if !strings.Contains(body, "--vt-palette-1:") || !strings.Contains(body, "--vt-palette-255:") {
				t.Errorf("fragment lacks the palette")
			}
			if got := h.Get("Content-Security-Policy"); got != "default-src 'none'; style-src 'unsafe-inline'" {
				t.Errorf("CSP = %q", got)
			}
			if got := h.Get("X-Watch-Slow"); got != tc.slow {
				t.Errorf("X-Watch-Slow = %q, want %q", got, tc.slow)
			}
			if ct := h.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Errorf("Content-Type = %q", ct)
			}
		})
	}
}

// TestWatchScreenUsesHubFrame: a screen the live reader already read is
// served as it is, with no second Rex call.
func TestWatchScreenUsesHubFrame(t *testing.T) {
	calls := 0
	rg := watchRig(t, func(context.Context, string, string, int64) (WatchReading, error) {
		calls++
		return WatchReading{HTML: []byte("direct")}, nil
	})
	rg.srv.hub.mu.Lock()
	rg.srv.hub.screens = map[string]*watchScreen{watchKey("app", "t1-2"): {ok: true, reading: WatchReading{HTML: []byte("from hub"), Slow: true}}}
	rg.srv.hub.mu.Unlock()
	code, body, h := get(rg, "/p/app/sessions/t1-2/watch/screen")
	if code != 200 || !strings.HasSuffix(body, "</style>from hub") || h.Get("X-Watch-Slow") != "1" || calls != 0 {
		t.Errorf("status %d body %q slow %q calls %d", code, body, h.Get("X-Watch-Slow"), calls)
	}
}

// TestWatchPing: the 204-and-lease row is load-bearing, and so are the GET
// row (405, never a lease) and the body row (a body is not read and changes
// nothing). The missing-session row proves no lease is made for a name that
// is not a session.
func TestWatchPing(t *testing.T) {
	tests := []struct {
		name   string
		method string
		target string
		want   int
		lease  int
	}{
		{name: "ping", method: "POST", target: "/p/app/sessions/t1-2/watch/ping", want: 204, lease: 1},
		{name: "get refused", method: "GET", target: "/p/app/sessions/t1-2/watch/ping", want: 405},
		{name: "put refused", method: "PUT", target: "/p/app/sessions/t1-2/watch/ping", want: 405},
		{name: "missing session", method: "POST", target: "/p/app/sessions/nope/watch/ping", want: 404},
		{name: "missing project", method: "POST", target: "/p/nope/sessions/t1-2/watch/ping", want: 404},
		{name: "post to the frame route", method: "POST", target: "/p/app/sessions/t1-2/watch/screen", want: 404},
		{name: "no api twin", method: "POST", target: "/api/p/app/sessions/t1-2/watch/ping", want: 404},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := watchRig(t, rexReading)
			rec := authReq{method: tc.method, target: tc.target, cookie: goodCookie()}.send(rg.h)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d\n%s", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want == 204 && rec.Body.Len() != 0 {
				t.Errorf("a 204 has a body: %q", rec.Body.String())
			}
			if tc.want == 405 && rec.Header().Get("Allow") != "POST" {
				t.Errorf("Allow = %q", rec.Header().Get("Allow"))
			}
			if n := rg.srv.watch.Count(watchKey("app", "t1-2"), testNow); n != tc.lease {
				t.Errorf("leases = %d, want %d", n, tc.lease)
			}
		})
	}
	t.Run("no cookie", func(t *testing.T) {
		rg := watchRig(t, rexReading)
		rec := authReq{method: "POST", target: "/p/app/sessions/t1-2/watch/ping"}.send(rg.h)
		if rec.Code == 204 || rg.srv.watch.Count(watchKey("app", "t1-2"), testNow) != 0 {
			t.Errorf("an unsigned ping got %d", rec.Code)
		}
	})
}

// TestSessionWatch: the Watch link shows only for a Watchable session; the
// session page must never ask Rex to decide it.
func TestSessionWatch(t *testing.T) {
	for _, watchable := range []bool{false, true} {
		t.Run(map[bool]string{false: "not watchable", true: "watchable"}[watchable], func(t *testing.T) {
			calls := 0
			rg := watchRig(t, func(context.Context, string, string, int64) (WatchReading, error) {
				calls++
				return WatchReading{}, nil
			})
			rg.sessions = []SessionView{{Name: "t1-2", Phase: "executing", HostRunning: true, Watchable: watchable}}
			code, body, _ := get(rg, "/p/app/sessions/t1-2")
			if code != 200 {
				t.Fatalf("status = %d", code)
			}
			if got := strings.Contains(body, `href="/p/app/sessions/t1-2/watch">Watch</a>`); got != watchable {
				t.Errorf("Watch link present = %v, want %v", got, watchable)
			}
			if calls != 0 {
				t.Errorf("the session page called WatchScreen %d times", calls)
			}
		})
	}
}
