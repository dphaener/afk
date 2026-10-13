package web

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/usagestate"
)

// otherToken is a well-formed token that is not the configured one.
const otherToken = "tok-ffffffffffffffffffffffffffffffff"

// goodCookie is worked out here, not taken from the package, so the test
// pins the documented value: lowercase hex SHA-256 of "afk-web:" + token.
func goodCookie(token string) string {
	sum := sha256.Sum256([]byte("afk-web:" + token))
	return hex.EncodeToString(sum[:])
}

// authReq describes one request for the auth tables.
type authReq struct {
	method string
	target string
	cookie string
	// rawCookie replaces the whole Cookie header when set.
	rawCookie string
	bearer    string
	// auth replaces the whole Authorization header when set.
	auth   string
	origin string
	host   string
}

func (a authReq) send(h http.Handler) *httptest.ResponseRecorder {
	method := a.method
	if method == "" {
		method = http.MethodGet
	}
	req := httptest.NewRequest(method, a.target, nil)
	if a.host != "" {
		req.Host = a.host
	}
	if a.cookie != "" {
		req.AddCookie(&http.Cookie{Name: cookieName, Value: a.cookie})
	}
	if a.rawCookie != "" {
		req.Header.Set("Cookie", a.rawCookie)
	}
	if a.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+a.bearer)
	}
	if a.auth != "" {
		req.Header.Set("Authorization", a.auth)
	}
	if a.origin != "" {
		req.Header.Set("Origin", a.origin)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func setCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName {
			return c
		}
	}
	return nil
}

// noSecrets fails when an answer repeats the token or the cookie value
// anywhere: body or headers (the Set-Cookie header aside, which is where the
// cookie value is meant to be).
func noSecrets(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	for _, secret := range []string{testToken, otherToken} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("the body repeats a token:\n%s", rec.Body.String())
		}
		for name, values := range rec.Header() {
			if strings.Contains(strings.Join(values, "\n"), secret) {
				t.Errorf("header %s repeats a token: %v", name, values)
			}
		}
	}
	if strings.Contains(rec.Body.String(), goodCookie(testToken)) {
		t.Errorf("the body repeats the cookie value:\n%s", rec.Body.String())
	}
}

func wantSecurityHeaders(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	want := map[string]string{
		"Cache-Control":           "no-store",
		"Referrer-Policy":         "same-origin",
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("header %s = %q, want %q", k, got, v)
		}
	}
}

// TestAuth is the table of who gets in. Every row also checks the security
// headers and that no answer repeats a secret.
//
// Load-bearing rows:
//   - "no token" and the three "wrong token" rows: 401, and no cookie handed
//     out.
//   - "cookie made from a different token": a cookie is only as good as the
//     token it was made from, so changing the token locks old cookies out.
//   - "cookie holding the token itself": the cookie must be the hash; the
//     plain token in a cookie is not accepted.
//   - "right token in query": served under /api with no redirect, cookie set.
//   - "wrong query, right cookie": a stale bookmark must not lock out a
//     browser that still holds a good cookie, and must not be handed a new
//     cookie on the strength of a wrong token.
//   - "empty token in query" and "empty bearer": an empty value must never
//     compare equal to anything.
func TestAuth(t *testing.T) {
	tests := []struct {
		name       string
		req        authReq
		wantStatus int
		wantCookie bool
	}{
		{name: "no token", req: authReq{target: "/api/"}, wantStatus: 401},
		{name: "wrong token in query", req: authReq{target: "/api/?token=" + otherToken}, wantStatus: 401},
		{name: "wrong token in cookie", req: authReq{target: "/api/", cookie: "0000"}, wantStatus: 401},
		{name: "wrong token in bearer", req: authReq{target: "/api/", bearer: otherToken}, wantStatus: 401},
		{name: "empty token in query", req: authReq{target: "/api/?token="}, wantStatus: 401},
		{name: "empty bearer", req: authReq{target: "/api/", auth: "Bearer "}, wantStatus: 401},
		{name: "token as basic auth", req: authReq{target: "/api/", auth: "Basic " + testToken}, wantStatus: 401},
		{name: "token as a bare authorization value", req: authReq{target: "/api/", auth: testToken}, wantStatus: 401},
		{name: "token with a prefix cut off", req: authReq{target: "/api/", bearer: testToken[:len(testToken)-1]}, wantStatus: 401},
		{name: "token with extra on the end", req: authReq{target: "/api/", bearer: testToken + "x"}, wantStatus: 401},
		{name: "token under another query name", req: authReq{target: "/api/?t=" + testToken}, wantStatus: 401},
		{name: "cookie made from a different token", req: authReq{target: "/api/", cookie: goodCookie(otherToken)}, wantStatus: 401},
		{name: "cookie holding the token itself", req: authReq{target: "/api/", cookie: testToken}, wantStatus: 401},
		{name: "cookie value in upper case", req: authReq{target: "/api/", cookie: strings.ToUpper(goodCookie(testToken))}, wantStatus: 401},
		{name: "right cookie under another name", req: authReq{target: "/api/", rawCookie: "afk=" + goodCookie(testToken)}, wantStatus: 401},
		{name: "cookie value as the bearer", req: authReq{target: "/api/", bearer: goodCookie(testToken)}, wantStatus: 401},

		{name: "right token in query", req: authReq{target: "/api/?token=" + testToken}, wantStatus: 200, wantCookie: true},
		{name: "right cookie", req: authReq{target: "/api/", cookie: goodCookie(testToken)}, wantStatus: 200},
		{name: "right bearer", req: authReq{target: "/api/", bearer: testToken}, wantStatus: 200},
		{name: "bearer scheme in lower case", req: authReq{target: "/api/", auth: "bearer " + testToken}, wantStatus: 200},
		{name: "wrong query, right cookie", req: authReq{target: "/api/?token=" + otherToken, cookie: goodCookie(testToken)}, wantStatus: 200},
		{name: "wrong cookie, right bearer", req: authReq{target: "/api/", cookie: "0000", bearer: testToken}, wantStatus: 200},
		{name: "wrong bearer, right query", req: authReq{target: "/api/?token=" + testToken, bearer: otherToken}, wantStatus: 200, wantCookie: true},
		{name: "right token on a POST query", req: authReq{method: "POST", target: "/api/p/app/pause?token=" + testToken}, wantStatus: 200, wantCookie: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rec := tc.req.send(rg.h)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d\n%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			wantSecurityHeaders(t, rec)
			noSecrets(t, rec)
			c := setCookie(rec)
			if (c != nil) != tc.wantCookie {
				t.Fatalf("cookie set = %v, want %v", c != nil, tc.wantCookie)
			}
			if rec.Code == http.StatusUnauthorized {
				m := decode(t, rec)
				if msg, _ := m["error"].(string); !strings.Contains(msg, "token") || !strings.Contains(msg, "config.toml") {
					t.Errorf("error = %q, want it to say what to send and where to find it", msg)
				}
				if len(rg.paused) != 0 {
					t.Errorf("a refused request reached a handler")
				}
			}
			if c != nil {
				if c.Value != goodCookie(testToken) {
					t.Errorf("cookie value = %q, want the hash of the token", c.Value)
				}
				if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.MaxAge != 180*24*60*60 {
					t.Errorf("cookie = %+v, want HttpOnly, SameSite=Lax, Path=/ and 180 days", c)
				}
				if c.Secure {
					t.Errorf("cookie is Secure with no public address set")
				}
			}
		})
	}
}

// TestAuthCookieSecure: the cookie is marked Secure only when the public address
// is https. The load-bearing row is plain http: a Secure cookie there is one
// the browser will not keep, which would lock the owner out.
func TestAuthCookieSecure(t *testing.T) {
	tests := []struct {
		name      string
		publicURL string
		want      bool
	}{
		{"no public address", "", false},
		{"http public address", "http://afk.example", false},
		{"https public address", "https://afk.example", true},
		{"https with a port", "https://afk.example:8443", true},
		{"a word that only starts like https", "httpsx://afk.example", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{PublicURL: tc.publicURL})
			rec := authReq{target: "/api/?token=" + testToken}.send(rg.h)
			c := setCookie(rec)
			if c == nil {
				t.Fatalf("no cookie set; status %d", rec.Code)
			}
			if c.Secure != tc.want {
				t.Errorf("Secure = %v, want %v", c.Secure, tc.want)
			}
		})
	}
}

// routeTarget turns a route's pattern into a real address under prefix.
func routeTarget(prefix string, rt route) string {
	path := strings.NewReplacer("{$}", "", "{project}", "app", "{id}", idBlocking).Replace(rt.path)
	return prefix + path
}

// TestAuthEveryRouteNeedsTheToken walks the route table, so a route added later
// is covered without a new row. For each route, every way of not having the
// token gets 401, and the right token gets through to the handler (which
// proves the address used here really is that route, and not a 401 for an
// address that does not exist).
//
// Load-bearing: the POST routes. A pause or an answer that went through
// without the token would let anyone who can reach the port stop a build or
// answer for the owner, so the test also checks nothing was changed.
func TestAuthEveryRouteNeedsTheToken(t *testing.T) {
	refused := []struct {
		name string
		with func(authReq) authReq
	}{
		{"no token", func(a authReq) authReq { return a }},
		{"wrong query", func(a authReq) authReq { a.target += "?token=" + otherToken; return a }},
		{"wrong cookie", func(a authReq) authReq { a.cookie = goodCookie(otherToken); return a }},
		{"wrong bearer", func(a authReq) authReq { a.bearer = otherToken; return a }},
	}
	if len(routes) != 9 {
		t.Fatalf("the route table has %d routes; the plan lists 9", len(routes))
	}
	for _, rt := range routes {
		target := routeTarget("/api", rt)
		for _, bad := range refused {
			t.Run(rt.method+" "+target+" "+bad.name, func(t *testing.T) {
				rg := newRig(t, Config{})
				rg.seedInbox()
				rec := bad.with(authReq{method: rt.method, target: target}).send(rg.h)
				if rec.Code != http.StatusUnauthorized {
					t.Fatalf("status = %d, want 401\n%s", rec.Code, rec.Body.String())
				}
				wantSecurityHeaders(t, rec)
				noSecrets(t, rec)
				if setCookie(rec) != nil {
					t.Errorf("a refused request was handed a cookie")
				}
				if _, ok := decode(t, rec)["error"]; !ok {
					t.Errorf("body = %s, want {\"error\": ...}", rec.Body.String())
				}
				if len(rg.paused)+len(rg.resumed) != 0 {
					t.Errorf("pause or resume ran without the token")
				}
				it, err := inbox.Store{Dir: inbox.Dir(rg.root)}.Get(idBlocking)
				must(t, err)
				if !it.Open() {
					t.Errorf("the item was answered without the token")
				}
			})
		}
		t.Run(rt.method+" "+target+" right bearer", func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.seedInbox()
			rec := authReq{method: rt.method, target: target, bearer: testToken}.send(rg.h)
			// The answer route gets 400 (no answer was sent); every other
			// route 200. Neither is the catch-all's 404.
			want := http.StatusOK
			if strings.HasSuffix(rt.path, "/answer") {
				want = http.StatusBadRequest
			}
			if rec.Code != want {
				t.Errorf("status = %d, want %d\n%s", rec.Code, want, rec.Body.String())
			}
		})
	}
}

// TestAuthUnauthorizedOutsideAPI: away from /api the refusal is a small page a
// person can act on. It must hold for addresses that do not exist too, so a
// stranger cannot map the site by telling 401 from 404.
func TestAuthUnauthorizedOutsideAPI(t *testing.T) {
	tests := []struct {
		name string
		req  authReq
	}{
		{"front page", authReq{target: "/"}},
		{"project page", authReq{target: "/p/app"}},
		{"an address that does not exist", authReq{target: "/no/such/page"}},
		{"wrong token in query", authReq{target: "/?token=" + otherToken}},
		{"wrong cookie", authReq{target: "/p/app", cookie: goodCookie(otherToken)}},
		{"a POST", authReq{method: "POST", target: "/p/app/pause"}},
		{"a path that only starts like the API", authReq{target: "/apiary"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rec := tc.req.send(rg.h)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
				t.Errorf("Content-Type = %q, want HTML", ct)
			}
			body := rec.Body.String()
			for _, want := range []string{"?token=", "config.toml", "<meta name=\"viewport\""} {
				if !strings.Contains(body, want) {
					t.Errorf("the page does not mention %q:\n%s", want, body)
				}
			}
			for _, banned := range []string{"<script", "http://", "https://", " src=", " href="} {
				if strings.Contains(body, banned) {
					t.Errorf("the page must be self-contained, but holds %q", banned)
				}
			}
			wantSecurityHeaders(t, rec)
			noSecrets(t, rec)
			if setCookie(rec) != nil {
				t.Errorf("a refused request was handed a cookie")
			}
			if len(rg.paused) != 0 {
				t.Errorf("a refused request reached a handler")
			}
		})
	}
}

// TestAuthTokenVisitRedirects: a page visit with ?token= sets the cookie and is
// sent on to the same address without the token.
//
// Load-bearing rows: the two "other site" rows. This middleware sees the
// path before the mux cleans it, so a path such as //other.example must not
// be echoed into Location, where a browser would read it as another site;
// the token visit would then be an open redirect that a stranger could aim
// at anyone who has the token. Also "under /api: no redirect": a script must
// get its answer.
func TestAuthTokenVisitRedirects(t *testing.T) {
	tests := []struct {
		name string
		// path and query are set on the request directly, so a path that
		// a request line could not carry can still be tried.
		path         string
		query        string
		method       string
		wantStatus   int
		wantLocation string
	}{
		{name: "front page", path: "/", query: "token=" + testToken, wantStatus: 303, wantLocation: "/"},
		{name: "project page", path: "/p/app", query: "token=" + testToken, wantStatus: 303, wantLocation: "/p/app"},
		{name: "item page", path: "/p/app/inbox/" + idBlocking, query: "token=" + testToken, wantStatus: 303, wantLocation: "/p/app/inbox/" + idBlocking},
		{name: "other query values are kept", path: "/p/app", query: "a=1&token=" + testToken + "&b=two+words", wantStatus: 303, wantLocation: "/p/app?a=1&b=two+words"},
		{name: "a second token value goes too", path: "/p/app", query: "token=" + testToken + "&token=" + otherToken, wantStatus: 303, wantLocation: "/p/app"},
		{name: "project name with a space stays escaped", path: "/p/my app", query: "token=" + testToken, wantStatus: 303, wantLocation: "/p/my%20app"},
		{name: "other site by double slash", path: "//other.example/x", query: "token=" + testToken, wantStatus: 303, wantLocation: "/other.example/x"},
		{name: "other site by backslash", path: `/\other.example/x`, query: "token=" + testToken, wantStatus: 303, wantLocation: "/other.example/x"},
		{name: "backslash later in the path is escaped", path: `/p\other.example`, query: "token=" + testToken, wantStatus: 303, wantLocation: "/p%5Cother.example"},
		{name: "under /api: no redirect", path: "/api/usage", query: "token=" + testToken, wantStatus: 200},
		{name: "a POST is not redirected", method: "POST", path: "/api/p/app/pause", query: "token=" + testToken, wantStatus: 200},
		{name: "wrong token: no redirect, no cookie", path: "/p/app", query: "token=" + otherToken, wantStatus: 401},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			method := tc.method
			if method == "" {
				method = http.MethodGet
			}
			req := httptest.NewRequest(method, "/", nil)
			req.URL.Path = tc.path
			req.URL.RawPath = ""
			req.URL.RawQuery = tc.query
			rec := httptest.NewRecorder()
			rg.h.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d\n%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			loc := rec.Header().Get("Location")
			if loc != tc.wantLocation {
				t.Errorf("Location = %q, want %q", loc, tc.wantLocation)
			}
			if strings.HasPrefix(loc, "//") || strings.HasPrefix(loc, `/\`) || strings.Contains(loc, "://") {
				t.Errorf("Location %q could send a browser to another site", loc)
			}
			noSecrets(t, rec)
			wantSecurityHeaders(t, rec)
			if got, want := setCookie(rec) != nil, tc.wantStatus != http.StatusUnauthorized; got != want {
				t.Errorf("cookie set = %v, want %v", got, want)
			}
			if tc.wantStatus == http.StatusSeeOther && rec.Body.Len() != 0 {
				t.Errorf("a redirect should have no body, got %q", rec.Body.String())
			}
		})
	}
}

// TestAuthPostOrigin is the cross-site check on POST.
//
// Load-bearing rows:
//   - "another site": refused with 403 although the token is right, and
//     the pause did not happen.
//   - "no Origin" and "same host": allowed, or curl and the page's own forms
//     would stop working.
//   - "public address behind a proxy": the request's Host is the proxy's
//     upstream address, not the public one. Without accepting the configured
//     public origin, every button pressed through such a proxy is refused.
//   - "public host, wrong scheme" and "public address not set": the proxy
//     allowance is exactly the configured origin and nothing wider.
func TestAuthPostOrigin(t *testing.T) {
	const public = "https://afk.example"
	tests := []struct {
		name       string
		publicURL  string
		host       string
		origin     string
		wantStatus int
	}{
		{name: "no Origin", host: "afk.local:4253", wantStatus: 200},
		{name: "same host", host: "afk.local:4253", origin: "http://afk.local:4253", wantStatus: 200},
		{name: "same host, other letter case", host: "afk.local:4253", origin: "http://AFK.local:4253", wantStatus: 200},
		{name: "another site", host: "afk.local:4253", origin: "https://other.example", wantStatus: 403},
		{name: "same name, another port", host: "afk.local:4253", origin: "http://afk.local:9999", wantStatus: 403},
		{name: "a site whose name starts with ours", host: "afk.local", origin: "http://afk.local.other.example", wantStatus: 403},
		{name: "Origin null", host: "afk.local:4253", origin: "null", wantStatus: 403},
		{name: "Origin that is not an address", host: "afk.local:4253", origin: "::not a url::", wantStatus: 403},
		{name: "public address behind a proxy", publicURL: public, host: "127.0.0.1:4253", origin: public, wantStatus: 200},
		{name: "public address with a trailing path", publicURL: public + "/", host: "127.0.0.1:4253", origin: public, wantStatus: 200},
		{name: "public host, wrong scheme", publicURL: public, host: "127.0.0.1:4253", origin: "http://afk.example", wantStatus: 403},
		{name: "public address set, another site", publicURL: public, host: "127.0.0.1:4253", origin: "https://other.example", wantStatus: 403},
		{name: "public address not set", host: "127.0.0.1:4253", origin: public, wantStatus: 403},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{PublicURL: tc.publicURL})
			rec := authReq{method: "POST", target: "/api/p/app/pause", cookie: goodCookie(testToken), host: tc.host, origin: tc.origin}.send(rg.h)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d\n%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			wantSecurityHeaders(t, rec)
			noSecrets(t, rec)
			wantCalls := 0
			if tc.wantStatus == http.StatusOK {
				wantCalls = 1
			}
			if len(rg.paused) != wantCalls {
				t.Errorf("pause calls = %d, want %d", len(rg.paused), wantCalls)
			}
			if tc.wantStatus == http.StatusForbidden {
				if msg, _ := decode(t, rec)["error"].(string); !strings.Contains(msg, "another site") {
					t.Errorf("error = %q", msg)
				}
			}
		})
	}
}

// TestAuthOriginDoesNotApplyToGET: the check is for requests that change
// something. A GET with a foreign Origin (a link followed from another site)
// must still be served, or notification links would break.
func TestAuthOriginDoesNotApplyToGET(t *testing.T) {
	rg := newRig(t, Config{})
	rec := authReq{target: "/api/", cookie: goodCookie(testToken), origin: "https://other.example"}.send(rg.h)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

// TestNew covers what New refuses. The load-bearing row is the empty token:
// a handler built without one would have nothing to compare against, and the
// only safe server in that case is none.
func TestNew(t *testing.T) {
	full := func() Deps {
		return Deps{
			LoadBuild: func(string) ([]masterplan.Task, ledger.Ledger, error) { return nil, ledger.Ledger{}, nil },
			Sessions:  func(string) []SessionView { return nil },
			Usage:     func() (usagestate.File, bool) { return usagestate.File{}, false },
			Pause:     func(string) error { return nil },
			Resume:    func(string) error { return nil },
		}
	}
	tests := []struct {
		name    string
		token   string
		mutate  func(*Deps)
		wantErr string
	}{
		{name: "everything given, no clock", token: testToken},
		{name: "empty token", token: "", wantErr: "token"},
		{name: "no LoadBuild", token: testToken, mutate: func(d *Deps) { d.LoadBuild = nil }, wantErr: "LoadBuild"},
		{name: "no Sessions", token: testToken, mutate: func(d *Deps) { d.Sessions = nil }, wantErr: "Sessions"},
		{name: "no Usage", token: testToken, mutate: func(d *Deps) { d.Usage = nil }, wantErr: "Usage"},
		{name: "no Pause", token: testToken, mutate: func(d *Deps) { d.Pause = nil }, wantErr: "Pause"},
		{name: "no Resume", token: testToken, mutate: func(d *Deps) { d.Resume = nil }, wantErr: "Resume"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := full()
			if tc.mutate != nil {
				tc.mutate(&d)
			}
			h, err := New(Config{Token: tc.token}, d)
			if tc.wantErr == "" {
				if err != nil || h == nil {
					t.Fatalf("New = %v, %v; want a handler", h, err)
				}
				// With no clock given, the real one is used and a
				// request works.
				rec := authReq{target: "/api/", bearer: testToken}.send(h)
				if rec.Code != http.StatusOK {
					t.Errorf("status = %d, want 200", rec.Code)
				}
				return
			}
			if err == nil || h != nil {
				t.Fatalf("New = %v, %v; want an error and no handler", h, err)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to name %q", err, tc.wantErr)
			}
			if strings.Contains(err.Error(), testToken) {
				t.Errorf("error repeats the token: %q", err)
			}
		})
	}
}
