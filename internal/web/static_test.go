package web

import (
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

const wantCSP = "default-src 'none'; script-src 'self'; connect-src 'self'; style-src 'self' 'unsafe-inline'; form-action 'self'"

// TestStatic walks the static route: the current address serves the file with
// the exact policy, the immutable cache header and a JavaScript type; a wrong
// hash, an unknown name and a request without the token do not.
//
// Load-bearing rows: "stale hash" and "unknown file" (404 with no-store, so a
// browser never keeps a miss for a year, and so nothing outside the embedded
// folder can be reached), "no token" (the files are behind the same guard as
// every page) and "current hash" (the CSP and the immutable header together,
// since the immutable header replaces the guard's no-store).
func TestStatic(t *testing.T) {
	rg := newRig(t, Config{})
	page := rg.pageDo("GET", "/usage", "").Body.String()
	m := regexp.MustCompile(`src="(/static/([0-9a-f]{12})/live\.js)"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("the page names no script:\n%s", page)
	}
	cur, hash := m[1], m[2]

	tests := []struct {
		name      string
		path      string
		token     bool
		status    int
		cache     string
		typePrefx string
	}{
		{name: "current hash", path: cur, token: true, status: 200, cache: "public, max-age=31536000, immutable", typePrefx: "text/javascript; charset=utf-8"},
		{name: "stale hash", path: "/static/000000000000/live.js", token: true, status: 404, cache: "no-store"},
		{name: "unknown file", path: "/static/" + hash + "/nope.js", token: true, status: 404, cache: "no-store"},
		{name: "a folder is not a file", path: "/static/" + hash + "/", token: true, status: 404, cache: "no-store"},
		{name: "no token", path: cur, token: false, status: 401, cache: "no-store"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			if tt.token {
				req.Header.Set("Authorization", "Bearer "+testToken)
			}
			rec := httptest.NewRecorder()
			rg.h.ServeHTTP(rec, req)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d", rec.Code, tt.status)
			}
			if got := rec.Header().Get("Cache-Control"); got != tt.cache {
				t.Errorf("Cache-Control = %q, want %q", got, tt.cache)
			}
			if got := rec.Header().Get("Content-Security-Policy"); got != wantCSP {
				t.Errorf("Content-Security-Policy = %q, want %q", got, wantCSP)
			}
			if tt.typePrefx != "" {
				if got := rec.Header().Get("Content-Type"); got != tt.typePrefx {
					t.Errorf("Content-Type = %q, want %q", got, tt.typePrefx)
				}
				if !strings.Contains(rec.Body.String(), "EventSource") {
					t.Errorf("the body is not live.js")
				}
			}
		})
	}
}
