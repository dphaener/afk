package ship

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestHealth checks the deploy check against a local test server.
//
// Load-bearing rows:
//   - "503": not healthy and no error. The deploy answered; that is the fact
//     the deploy timeout counts on. An error here would be dropped by Advance
//     the same way, but would hide the difference in any log.
//   - "204": any 2xx is healthy, not only 200.
//   - "404" and "500": nothing outside 2xx is healthy.
//   - every row: exactly one GET per call. The daemon's tick is the retry.
func TestHealth(t *testing.T) {
	tests := []struct {
		status int
		want   bool
	}{
		{status: 200, want: true},
		{status: 204, want: true},
		{status: 299, want: true},
		{status: 304, want: false},
		{status: 404, want: false},
		{status: 500, want: false},
		{status: 503, want: false},
	}
	for _, tt := range tests {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			var hits atomic.Int32
			var method atomic.Value
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				method.Store(r.Method)
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte("body the check does not care about"))
			}))
			defer srv.Close()

			healthy, err := NewHealth(srv.Client())(srv.URL + "/up")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if healthy != tt.want {
				t.Errorf("status %d: healthy = %v, want %v", tt.status, healthy, tt.want)
			}
			if hits.Load() != 1 || method.Load() != http.MethodGet {
				t.Errorf("made %d request(s) with method %v; want one GET", hits.Load(), method.Load())
			}
		})
	}
}

// TestHealthNoAnswer covers a deploy that does not answer at all, and what
// the error may say.
//
// The load-bearing part is the secret: a health URL can carry a password or
// a token, net/http quotes the whole URL in its errors, and this error can be
// logged. None of the three rows may leak it.
func TestHealthNoAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := strings.TrimPrefix(srv.URL, "http://")
	srv.Close() // nothing listens there now

	tests := []struct {
		name string
		url  string
	}{
		{name: "connection refused", url: "http://owner:hunter2@" + addr + "/up?token=hunter2"},
		{name: "not a url", url: "http://owner:hunter2@bad host/%zz?token=hunter2"},
		{name: "scheme nothing speaks", url: "gopher://owner:hunter2@" + addr + "/?token=hunter2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			healthy, err := NewHealth(nil)(tt.url)
			if healthy {
				t.Error("healthy with no answer")
			}
			if err == nil {
				t.Fatal("no error for a check that got no answer")
			}
			if strings.Contains(err.Error(), "hunter2") {
				t.Errorf("the error leaks the secret in the URL: %q", err)
			}
		})
	}
}

// TestHealthFollowsRedirect pins that a health URL that redirects is judged
// by where it ends up, which is what a browser would show the owner.
func TestHealthFollowsRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			http.Redirect(w, r, "/up", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	healthy, err := NewHealth(srv.Client())(srv.URL + "/old")
	if err != nil || healthy {
		t.Errorf("healthy = %v, err = %v; want the 503 behind the redirect to count", healthy, err)
	}
}
