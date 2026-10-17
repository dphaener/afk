package web

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

const wantCSP = "default-src 'none'; script-src 'self'; connect-src 'self'; style-src 'self' 'unsafe-inline'; form-action 'self'"

// TestStatic walks the static route: the current address serves the file with
// the exact policy, the immutable cache header and a JavaScript type; a wrong
// hash, an unknown name and a request without a cookie do not.
//
// Load-bearing rows: "stale hash" and "unknown file" (404 with no-store, so a
// browser never keeps a miss for a year, and so nothing outside the embedded
// folder can be reached), "no cookie" (the files are behind the same guard as
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
		cookie    bool
		status    int
		cache     string
		typePrefx string
	}{
		{name: "current hash", path: cur, cookie: true, status: 200, cache: "public, max-age=31536000, immutable", typePrefx: "text/javascript; charset=utf-8"},
		{name: "stale hash", path: "/static/000000000000/live.js", cookie: true, status: 404, cache: "no-store"},
		{name: "unknown file", path: "/static/" + hash + "/nope.js", cookie: true, status: 404, cache: "no-store"},
		{name: "a folder is not a file", path: "/static/" + hash + "/", cookie: true, status: 404, cache: "no-store"},
		{name: "no cookie", path: cur, cookie: false, status: 401, cache: "no-store"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			if tt.cookie {
				req.AddCookie(&http.Cookie{Name: cookieName, Value: goodCookie()})
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

// TestStaticAfkCSS walks the design system's stylesheet through the static
// route. The expected address is computed from the embedded bytes here, not
// read back from the server, so a wrong hash in the server cannot pass.
//
// Load-bearing rows: "current hash" (the stylesheet is served at the address
// its bytes earn, as text/css, with the immutable header and the CSP) and
// "canvas-only rules absent" (the served body holds no `.afk.screen` and no
// `.is-hover`, `.is-focus` or `.is-active` selector: those only exist for the
// design canvas, and a page that used them would show a state no real
// pointer or keyboard produces). "wrong hash" is a 404 with no-store, as for
// every other asset, so a stale address cannot be cached as a miss.
func TestStaticAfkCSS(t *testing.T) {
	body, err := staticFiles.ReadFile("static/afk.css")
	if err != nil {
		t.Fatalf("the binary holds no afk.css: %v", err)
	}
	sum := sha256.Sum256(body)
	cur := "/static/" + hex.EncodeToString(sum[:])[:12] + "/afk.css"

	rg := newRig(t, Config{})
	tests := []struct {
		name   string
		path   string
		cookie bool
		status int
		cache  string
	}{
		{name: "current hash", path: cur, cookie: true, status: 200, cache: "public, max-age=31536000, immutable"},
		{name: "wrong hash", path: "/static/000000000000/afk.css", cookie: true, status: 404, cache: "no-store"},
		// The sign-in form links this sheet, so it is served without the
		// cookie at its current address (see TestStaticRefusedPageStyles).
		{name: "no cookie", path: cur, cookie: false, status: 200, cache: "public, max-age=31536000, immutable"},
		{name: "no cookie, wrong hash", path: "/static/000000000000/afk.css", cookie: false, status: 401, cache: "no-store"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			if tt.cookie {
				req.AddCookie(&http.Cookie{Name: cookieName, Value: goodCookie()})
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
			if tt.status != 200 {
				return
			}
			if got := rec.Header().Get("Content-Type"); got != "text/css; charset=utf-8" {
				t.Errorf("Content-Type = %q, want text/css; charset=utf-8", got)
			}
			served := rec.Body.String()
			if !strings.Contains(served, ".page {") {
				t.Errorf("the body is not afk.css")
			}
			for _, banned := range []string{".afk.screen", ".is-hover", ".is-focus", ".is-active"} {
				if strings.Contains(served, banned) {
					t.Errorf("the body holds canvas-only %q", banned)
				}
			}
		})
	}
}

// TestStaticRefusedPageStyles covers the one exemption from the cookie: the
// two stylesheets the sign-in form links, at their current addresses, so the
// page a browser without a cookie shows has the design's look.
//
// Load-bearing rows: "afk.css" and "page.css" (served without a cookie, or
// the sign-in form is unstyled in a real browser, which no handler test
// would notice), and every refused row: live.js, a stale hash, an unknown
// name, a POST and a stylesheet's name under another hash must still get the
// refusal, so the exemption is two exact addresses and not a folder.
func TestStaticRefusedPageStyles(t *testing.T) {
	rg := newRig(t, Config{})
	refusedRec := httptest.NewRecorder()
	rg.h.ServeHTTP(refusedRec, httptest.NewRequest("GET", "/", nil))
	refused := refusedRec.Body.String()
	links := regexp.MustCompile(`<link rel="stylesheet" href="(/static/[0-9a-f]{12}/(afk|page)\.css)">`).FindAllStringSubmatch(refused, -1)
	if len(links) != 2 {
		t.Fatalf("the sign-in form links %d stylesheets, want afk.css and page.css:\n%s", len(links), refused)
	}
	afkCSS, pageCSS := links[0][1], links[1][1]
	livePage := rg.pageDo("GET", "/usage", "").Body.String()
	liveJS := regexp.MustCompile(`/static/[0-9a-f]{12}/live\.js`).FindString(livePage)
	if liveJS == "" {
		t.Fatalf("the page names no live.js")
	}
	hash := strings.Split(afkCSS, "/")[2]

	tests := []struct {
		name   string
		method string
		path   string
		status int
	}{
		{name: "afk.css", method: "GET", path: afkCSS, status: 200},
		{name: "page.css", method: "GET", path: pageCSS, status: 200},
		{name: "live.js is not exempt", method: "GET", path: liveJS, status: 401},
		{name: "stale hash", method: "GET", path: "/static/000000000000/page.css", status: 401},
		{name: "unknown name under the sheet's hash", method: "GET", path: "/static/" + hash + "/nope.css", status: 401},
		{name: "page.css under afk.css's hash", method: "GET", path: "/static/" + hash + "/page.css", status: 401},
		{name: "a POST to the sheet", method: "POST", path: afkCSS, status: 401},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			rg.h.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d", rec.Code, tt.status)
			}
			if got := rec.Header().Get("Content-Security-Policy"); got != wantCSP {
				t.Errorf("Content-Security-Policy = %q, want %q", got, wantCSP)
			}
			if setCookie(rec) != nil {
				t.Errorf("a request without a cookie was handed a cookie")
			}
			if tt.status == 200 && rec.Header().Get("Content-Type") != "text/css; charset=utf-8" {
				t.Errorf("Content-Type = %q, want text/css", rec.Header().Get("Content-Type"))
			}
		})
	}
}

// TestLiveJSButtonsRegion pins the opt-in that lets the Refresh usage region
// be swapped while its button has focus. Load-bearing: the page marks the
// region, and live.js knows the attribute; without both the button would
// stay on "Refreshing" for as long as the owner's focus stayed on it.
func TestLiveJSButtonsRegion(t *testing.T) {
	rg := newRig(t, Config{})
	page := rg.pageDo("GET", "/usage", "").Body.String()
	if !strings.Contains(page, "data-live-buttons") {
		t.Errorf("the usage page does not mark its refresh region data-live-buttons")
	}
	js, err := staticFiles.ReadFile("static/live.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(js), "data-live-buttons") {
		t.Errorf("live.js does not know data-live-buttons")
	}
}
