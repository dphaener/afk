package web

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/usagestate"
	"github.com/dphaener/afk/internal/webpass"
)

// oldTokenQuery is the query a token query visit (`token=` in the address) carried before the page had a
// password. The token is gone; the rows that send it prove it opens nothing.
func oldTokenQuery(v string) string { return url.Values{"token": {v}}.Encode() }

// bearerHeader is an Authorization header in the bearer scheme, the other
// way in the token had. The scheme word is written in lower case because
// HTTP scheme names match without regard to case (the old code accepted
// either), so this is the same request as the capitalised spelling.
func bearerHeader(v string) string { return "bearer " + v }

// authReq describes one request for the auth tables.
type authReq struct {
	method string
	target string
	cookie string
	// rawCookie replaces the whole Cookie header when set.
	rawCookie string
	// auth sets the Authorization header when set.
	auth   string
	origin string
	host   string
	// form, when set, is sent as a form body instead of the JSON confirm.
	form url.Values
}

func (a authReq) send(h http.Handler) *httptest.ResponseRecorder {
	method := a.method
	if method == "" {
		method = http.MethodGet
	}
	// A pause or resume needs the confirm in its body, and these tests are
	// about the sign-in and the origin, so every POST carries it: a refusal
	// then cannot be the missing confirm.
	var body io.Reader
	contentType := ""
	switch {
	case a.form != nil:
		body, contentType = strings.NewReader(a.form.Encode()), formType
	case method == http.MethodPost:
		body, contentType = strings.NewReader(`{"confirm":true}`), "application/json"
	}
	req := httptest.NewRequest(method, a.target, body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if a.host != "" {
		req.Host = a.host
	}
	if a.cookie != "" {
		req.AddCookie(&http.Cookie{Name: cookieName, Value: a.cookie})
	}
	if a.rawCookie != "" {
		req.Header.Set("Cookie", a.rawCookie)
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

// signInForm is the form body of a sign-in with password and next.
func signInForm(password, next string) url.Values {
	return url.Values{"password": {password}, "next": {next}}
}

func setCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == cookieName {
			return c
		}
	}
	return nil
}

// noSecrets fails when an answer repeats a password, the stored record's
// key or salt, or the cookie value anywhere: body or headers (the Set-Cookie
// header aside, which is where the cookie value is meant to be).
func noSecrets(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	secrets := []string{testPassword, "pw-ffffffffffffffffffffffffffffffff"}
	for _, r := range []webpass.Record{testRecord, otherRecord} {
		secrets = append(secrets, hex.EncodeToString(r.Key), base64.StdEncoding.EncodeToString(r.Key), base64.StdEncoding.EncodeToString(r.Salt))
	}
	for _, secret := range secrets {
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("the body repeats a secret %q:\n%s", secret, rec.Body.String())
		}
		for name, values := range rec.Header() {
			if name == "Set-Cookie" {
				continue
			}
			if strings.Contains(strings.Join(values, "\n"), secret) {
				t.Errorf("header %s repeats a secret: %v", name, values)
			}
		}
	}
	if strings.Contains(rec.Body.String(), goodCookie()) {
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
		"Content-Security-Policy": "default-src 'none'; script-src 'self'; connect-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self'; manifest-src 'self'; worker-src 'self'; form-action 'self'",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("header %s = %q, want %q", k, got, v)
		}
	}
}

// TestSignedIn is the pure cookie check.
//
// Load-bearing rows:
//   - "cookie from before a password change": a new password has a new salt
//     and key, and every old cookie must stop matching.
//   - "no password set" and "password file unreadable": with no record
//     nobody is signed in, whatever the cookie holds.
//   - "empty cookie, invalid record": an invalid record's value is "", and
//     an empty cookie must never pass as equal to it.
func TestSignedIn(t *testing.T) {
	tests := []struct {
		name   string
		cookie string
		rec    webpass.Record
		err    error
		want   bool
	}{
		{name: "right cookie", cookie: goodCookie(), rec: testRecord, want: true},
		{name: "cookie from before a password change", cookie: otherCookie(), rec: testRecord},
		{name: "no cookie", cookie: "", rec: testRecord},
		{name: "no password set", cookie: goodCookie(), rec: testRecord, err: webpass.ErrNoPassword},
		{name: "password file unreadable", cookie: goodCookie(), rec: testRecord, err: errors.New("damaged")},
		{name: "empty cookie, invalid record", cookie: "", rec: webpass.Record{}},
		{name: "any cookie, invalid record", cookie: goodCookie(), rec: webpass.Record{}},
		{name: "cookie holding the password", cookie: testPassword, rec: testRecord},
		{name: "cookie value in upper case", cookie: strings.ToUpper(goodCookie()), rec: testRecord},
		{name: "cookie value cut short", cookie: goodCookie()[:len(goodCookie())-1], rec: testRecord},
		{name: "cookie value with extra on the end", cookie: goodCookie() + "0", rec: testRecord},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := signedIn(tc.cookie, tc.rec, tc.err); got != tc.want {
				t.Errorf("signedIn = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestLockout is the pure wrong-password counter, as a list of steps on one
// counter.
//
// Load-bearing rows: "fifth wrong locks, even a right one waits" (the lock
// holds for the minute and the caller must not hash), "after the minute the
// count starts again" (one more wrong try after the lock must not lock again
// at once), and "a right password resets the count" (only wrong passwords
// in a row lock, or the owner's own typos would add up over months).
func TestLockout(t *testing.T) {
	type step struct {
		at          time.Duration // after testNow
		op          string        // "allowed", "failed" or "succeeded"
		wantAllowed bool          // for "allowed"
	}
	fail := func(at time.Duration) []step {
		return []step{{at: at, op: "allowed", wantAllowed: true}, {at: at, op: "failed"}}
	}
	repeat := func(n int, at time.Duration) []step {
		var out []step
		for range n {
			out = append(out, fail(at)...)
		}
		return out
	}
	join := func(parts ...[]step) []step {
		var out []step
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	tests := []struct {
		name  string
		steps []step
	}{
		{name: "a fresh counter allows", steps: []step{{op: "allowed", wantAllowed: true}}},
		{name: "four wrong still allow", steps: join(repeat(4, 0), []step{{op: "allowed", wantAllowed: true}})},
		{name: "fifth wrong locks, even a right one waits", steps: join(repeat(5, 0), []step{{at: 59 * time.Second, op: "allowed", wantAllowed: false}})},
		{name: "the lock ends after the minute", steps: join(repeat(5, 0), []step{{at: time.Minute, op: "allowed", wantAllowed: true}})},
		{name: "after the minute the count starts again", steps: join(repeat(5, 0), repeat(1, time.Minute), []step{{at: time.Minute, op: "allowed", wantAllowed: true}})},
		{name: "after the minute five more lock again", steps: join(repeat(5, 0), repeat(5, time.Minute), []step{{at: time.Minute + time.Second, op: "allowed", wantAllowed: false}})},
		{name: "a right password resets the count", steps: join(repeat(4, 0), []step{{op: "succeeded"}}, repeat(4, 0), []step{{op: "allowed", wantAllowed: true}})},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var l lockout
			for i, st := range tc.steps {
				now := testNow.Add(st.at)
				switch st.op {
				case "allowed":
					if got := l.allowed(now); got != st.wantAllowed {
						t.Fatalf("step %d: allowed = %v, want %v", i, got, st.wantAllowed)
					}
				case "failed":
					l.failed(now)
				case "succeeded":
					l.succeeded()
				}
			}
		})
	}
}

// TestReturnTo is the pure choice of where a sign-in goes on to.
//
// Load-bearing rows: every "other site" row (a browser reads each as
// another site, and the sign-in would be an open redirect a stranger could
// aim at the owner), "under /api" (never land a browser on JSON), and "the
// sign-in itself" (a loop back to the form, with a hostile next inside).
func TestReturnTo(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "front page", raw: "/", want: "/"},
		{name: "a page with its query kept", raw: "/p/x?y=1", want: "/p/x?y=1"},
		{name: "a project with a space stays escaped", raw: "/p/my%20app", want: "/p/my%20app"},
		{name: "empty", raw: "", want: "/"},
		{name: "other site by double slash", raw: "//evil.example/", want: "/"},
		{name: "other site by backslash", raw: `/\evil.example`, want: "/"},
		{name: "other site by full address", raw: "https://evil.example/", want: "/"},
		{name: "other site by escaped slashes", raw: "/%2F%2Fevil", want: "/"},
		{name: "other site by escaped backslash", raw: "/%5Cevil.example", want: "/"},
		{name: "a script address", raw: "javascript:alert(1)", want: "/"},
		{name: "a relative path", raw: "p/x", want: "/"},
		{name: "under /api", raw: "/api/p/x", want: "/"},
		{name: "the API root", raw: "/api", want: "/"},
		{name: "under /api by a dot path", raw: "/p/../api/p/x", want: "/"},
		{name: "the sign-in itself", raw: "/signin?next=//evil", want: "/"},
		{name: "the sign-out", raw: "/signout", want: "/"},
		{name: "a fragment is dropped", raw: "/p/x#top", want: "/p/x"},
		{name: "a backslash later in the path is escaped", raw: `/p\evil.example`, want: "/p%5Cevil.example"},
		{name: "a control character", raw: "/p/x\x00", want: "/"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := returnTo(tc.raw)
			if got != tc.want {
				t.Errorf("returnTo(%q) = %q, want %q", tc.raw, got, tc.want)
			}
			if strings.HasPrefix(got, "//") || strings.HasPrefix(got, `/\`) || strings.Contains(got, "://") {
				t.Errorf("returnTo(%q) = %q could send a browser to another site", tc.raw, got)
			}
		})
	}
}

// TestAuth is the table of who gets in under /api. Every row also checks the
// security headers and that no answer repeats a secret.
//
// Load-bearing rows:
//   - "no cookie" and "cookie from before a password change": 401, and no
//     cookie handed out.
//   - "old token query visit" and the bearer rows: the token's ways in are gone
//     and open nothing, not even with the right cookie value as the bearer.
//   - "right cookie, no password set": a cookie is only good while its
//     record exists; with no record nobody is signed in.
//   - "cookie holding the password": the cookie must be the derived value.
func TestAuth(t *testing.T) {
	tests := []struct {
		name       string
		req        authReq
		passErr    error
		wantStatus int
	}{
		{name: "no cookie", req: authReq{target: "/api/"}, wantStatus: 401},
		{name: "cookie from before a password change", req: authReq{target: "/api/", cookie: otherCookie()}, wantStatus: 401},
		{name: "wrong cookie", req: authReq{target: "/api/", cookie: "0000"}, wantStatus: 401},
		{name: "cookie holding the password", req: authReq{target: "/api/", cookie: testPassword}, wantStatus: 401},
		{name: "cookie value in upper case", req: authReq{target: "/api/", cookie: strings.ToUpper(goodCookie())}, wantStatus: 401},
		{name: "right cookie under another name", req: authReq{target: "/api/", rawCookie: "afk=" + goodCookie()}, wantStatus: 401},
		{name: "old token query visit", req: authReq{target: "/api/?" + oldTokenQuery("tok-4f9a1c77d2e84b0a9c3d5e6f70812345")}, wantStatus: 401},
		{name: "old token query visit holding the password", req: authReq{target: "/api/?" + oldTokenQuery(testPassword)}, wantStatus: 401},
		{name: "bearer header", req: authReq{target: "/api/", auth: bearerHeader("tok-4f9a1c77d2e84b0a9c3d5e6f70812345")}, wantStatus: 401},
		{name: "bearer header holding the cookie value", req: authReq{target: "/api/", auth: bearerHeader(goodCookie())}, wantStatus: 401},
		{name: "bearer header holding the password", req: authReq{target: "/api/", auth: bearerHeader(testPassword)}, wantStatus: 401},
		{name: "right cookie, no password set", req: authReq{target: "/api/", cookie: goodCookie()}, passErr: webpass.ErrNoPassword, wantStatus: 401},
		{name: "right cookie, password file unreadable", req: authReq{target: "/api/", cookie: goodCookie()}, passErr: errors.New("damaged"), wantStatus: 401},

		{name: "right cookie", req: authReq{target: "/api/", cookie: goodCookie()}, wantStatus: 200},
		{name: "right cookie with a stray bearer header", req: authReq{target: "/api/", cookie: goodCookie(), auth: bearerHeader("x")}, wantStatus: 200},
		{name: "right cookie on a POST", req: authReq{method: "POST", target: "/api/p/app/pause", cookie: goodCookie()}, wantStatus: 200},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			if tc.passErr != nil {
				rg.setPassword(webpass.Record{}, tc.passErr)
			}
			rec := tc.req.send(rg.h)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d\n%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			wantSecurityHeaders(t, rec)
			noSecrets(t, rec)
			if setCookie(rec) != nil {
				t.Fatalf("a cookie was handed out outside the sign-in")
			}
			if rec.Code == http.StatusUnauthorized {
				msg, _ := decode(t, rec)["error"].(string)
				if !strings.Contains(msg, "sign in") || strings.Contains(strings.ToLower(msg), "token") {
					t.Errorf("error = %q, want it to say to sign in, and nothing about a token", msg)
				}
				if len(rg.paused) != 0 {
					t.Errorf("a refused request reached a handler")
				}
			}
		})
	}
}

// routeTarget turns a route's pattern into a real address under prefix.
func routeTarget(prefix string, rt route) string {
	// {id} is an inbox item's id under /inbox and a master-plan task's id
	// under /tasks, and {name} a session's name; each must be one the rig
	// holds, so the right cookie reaches the handler and not a 404.
	id := idBlocking
	if strings.Contains(rt.path, "/tasks/") {
		id = "T1.2"
	}
	path := strings.NewReplacer("{$}", "", "{project}", "app", "{id}", id, "{name}", "t1-2").Replace(rt.path)
	return prefix + path
}

// refusedWays are the ways of not being signed in that the route walks try:
// no cookie, a cookie from before a password change, and the token's two old
// ways in, which must open nothing now.
var refusedWays = []struct {
	name string
	with func(authReq) authReq
}{
	{"no cookie", func(a authReq) authReq { return a }},
	{"cookie from before a password change", func(a authReq) authReq { a.cookie = otherCookie(); return a }},
	{"old token query", func(a authReq) authReq {
		sep := "?"
		if strings.Contains(a.target, "?") {
			sep = "&"
		}
		a.target += sep + oldTokenQuery("tok-4f9a1c77d2e84b0a9c3d5e6f70812345")
		return a
	}},
	{"bearer header", func(a authReq) authReq { a.auth = bearerHeader(goodCookie()); return a }},
}

// TestAuthEveryRouteNeedsACookie walks the route table, so a route added
// later is covered without a new row. For each route, every way of not
// being signed in gets 401, and the right cookie gets through to the handler
// (which proves the address used here really is that route, and not a 401
// for an address that does not exist).
//
// Load-bearing: the POST routes. A pause or an answer that went through
// without a sign-in would let anyone who can reach the port stop a build or
// answer for the owner, so the test also checks nothing was changed.
func TestAuthEveryRouteNeedsACookie(t *testing.T) {
	if len(routes) != 25 {
		t.Fatalf("the route table has %d routes; the plan lists 25", len(routes))
	}
	for _, rt := range routes {
		target := routeTarget("/api", rt)
		for _, bad := range refusedWays {
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
					t.Errorf("pause or resume ran without a sign-in")
				}
				if len(rg.tokenCalls)+len(rg.statusCalls) != 0 {
					t.Errorf("a task's tokens were read or its status set without a sign-in")
				}
				if n := rg.sessionCalls(); n != 0 {
					t.Errorf("a session action ran %d times without a sign-in", n)
				}
				if len(rg.startCalls) != 0 {
					t.Errorf("a planning session was started without a sign-in: %v", rg.startCalls)
				}
				if len(rg.checkCalls)+len(rg.addedCalls) != 0 {
					t.Errorf("a folder was checked or noted as being planned without a sign-in: %v %v", rg.checkCalls, rg.addedCalls)
				}
				if rg.createCalls != 0 {
					t.Errorf("a folder was made without a sign-in: %q", rg.createText)
				}
				if len(rg.approveCalls) != 0 {
					t.Errorf("a plan was approved without a sign-in: %v", rg.approveCalls)
				}
				if len(rg.addProjectCalls)+len(rg.removeProjectCalls) != 0 {
					t.Errorf("a project was added or removed without a sign-in: %v %v", rg.addProjectCalls, rg.removeProjectCalls)
				}
				rg.waitRefresh()
				if n := rg.refreshCalls.Load(); n != 0 {
					t.Errorf("a usage reading ran %d times without a sign-in", n)
				}
				it, err := inbox.Store{Dir: inbox.Dir(rg.root)}.Get(idBlocking)
				must(t, err)
				if !it.Open() {
					t.Errorf("the item was answered without a sign-in")
				}
			})
		}
		t.Run(rt.method+" "+target+" right cookie", func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.seedInbox()
			// The status POST carries the confirm (send adds it) but no
			// status, so the fake answers as the real seam would: refused.
			rg.statusResult = TaskStatusResult{Outcome: StatusRefused, Text: "unknown status"}
			rec := authReq{method: rt.method, target: target, cookie: goodCookie()}.send(rg.h)
			// The answer route gets 400 (no answer was sent) and the status
			// route 400 (no status was sent, so the seam refuses it); every
			// other route 200, except the usage refresh, which answers 202
			// (a reading started). Of the session actions, clean and label
			// reach their seam (200, done); restart is refused, because the
			// rig's t1-2 is a healthy running host (400), and starting a
			// session is refused for its missing name (400). Approving the
			// plan reaches its seam, whose rig answer is approved (200).
			// Planning a new project is refused for its missing path (400),
			// and so is the GitHub answer, since no folder was offered
			// (400, not_offered). Adding a project is refused for its missing
			// folder (400), and removing one for its missing name (400,
			// not_listed), both before their seam. None is the catch-all's 404.
			want := http.StatusOK
			if strings.HasSuffix(rt.path, "/answer") || strings.HasSuffix(rt.path, "/status") ||
				strings.HasSuffix(rt.path, "/restart") || strings.HasSuffix(rt.path, "/plan/change") || rt.path == "/plan/new" || rt.path == "/plan/new/github" ||
				rt.path == "/projects/add" || rt.path == "/projects/remove" ||
				(rt.method == http.MethodPost && rt.path == "/p/{project}/sessions") {
				want = http.StatusBadRequest
			}
			if rt.path == "/usage/refresh" {
				want = http.StatusAccepted
			}
			if rec.Code != want {
				t.Errorf("status = %d, want %d\n%s", rec.Code, want, rec.Body.String())
			}
		})
	}
}

// TestAuthPlanReplyRouteNeedsACookie covers the one POST route that is not
// in the route table (it has no page twin), with the same refusals.
// Load-bearing: the fake PlanReply must not run, or a stranger could type
// into the planning session.
func TestAuthPlanReplyRouteNeedsACookie(t *testing.T) {
	for _, bad := range refusedWays {
		t.Run(bad.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rec := bad.with(authReq{method: http.MethodPost, target: "/api/p/app/plan/chat"}).send(rg.h)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401\n%s", rec.Code, rec.Body.String())
			}
			if len(rg.replies) != 0 {
				t.Errorf("PlanReply ran without a sign-in")
			}
		})
	}
}

// TestAuthPlanChatPageRoutesNeedACookie covers the chat screen's two page
// routes, which are not in the route table either, with the same refusals.
// Load-bearing: the POST row, where the fake PlanReply must not run (a
// stranger could type into the planning session), and the GET rows, where
// PlanChat must not run (a stranger could read the conversation).
func TestAuthPlanChatPageRoutesNeedACookie(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		for _, bad := range refusedWays {
			t.Run(method+"_"+bad.name, func(t *testing.T) {
				rg := newRig(t, Config{})
				rec := bad.with(authReq{method: method, target: "/p/app/plan/chat"}).send(rg.h)
				if rec.Code != http.StatusUnauthorized {
					t.Fatalf("status = %d, want 401\n%s", rec.Code, rec.Body.String())
				}
				if len(rg.replies) != 0 || len(rg.chatCalls) != 0 {
					t.Errorf("the chat was read or replied to without a sign-in: %v %v", rg.chatCalls, rg.replies)
				}
			})
		}
	}
}

// TestAuthPlanChangePageNeedsACookie covers the change page's GET, which is
// not in the route table. Load-bearing: the seam must stay untouched, since
// a stranger who could reach the page could otherwise learn whether a
// planning session is open, and the confirm card is one request from
// starting one.
func TestAuthPlanChangePageNeedsACookie(t *testing.T) {
	for _, bad := range refusedWays {
		t.Run(bad.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rec := bad.with(authReq{target: "/p/app/plan/change?mode=extend"}).send(rg.h)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401\n%s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "Extend the plan") || len(rg.startCalls) != 0 {
				t.Errorf("the page was drawn or the seam called without a sign-in")
			}
		})
	}
}

// signInStylesheet matches the sign-in form's two stylesheet links.
var signInStylesheet = regexp.MustCompile(`<link rel="stylesheet" href="/static/[0-9a-f]{12}/(afk|page)\.css">`)

// wantSignInForm checks that a body is the sign-in form, alone: the design's
// pieces, a real label, a password field the browser may fill, the hidden
// next holding wantNext, and nothing else (no bars, no script, no project).
func wantSignInForm(t *testing.T, body, wantNext string) {
	t.Helper()
	for _, want := range []string{
		`<meta name="viewport"`, `<body class="afk">`, `class="wordmark"`, "<h1>This page is private</h1>",
		`<form method="post" action="/signin"`,
		`<label class="field__label" for="signin-password">Password</label>`,
		`<input class="input" type="password" id="signin-password" name="password" autocomplete="current-password" required`,
		`<input type="hidden" name="next" value="` + html.EscapeString(wantNext) + `">`,
		`<button type="submit" class="btn btn--primary">Sign in</button>`,
		`<p class="meta">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the form does not hold %q:\n%s", want, body)
		}
	}
	if n := len(signInStylesheet.FindAllString(body, -1)); n != 2 {
		t.Errorf("the form links %d of its two stylesheets:\n%s", n, body)
	}
	for _, banned := range []string{"<script", "app-bar", "topnav", "app-bar--desktop", "my app", "data-live"} {
		if strings.Contains(body, banned) {
			t.Errorf("the sign-in form holds %q:\n%s", banned, body)
		}
	}
}

// TestAuthUnauthorizedOutsideAPI: away from /api a request without a cookie
// gets the sign-in form, at the address asked for and with status 401. It
// must hold for addresses that do not exist too, so a stranger cannot map
// the site by telling 401 from 404.
//
// Load-bearing: "an address that does not exist" (no 404 without a
// sign-in), "a POST" (next is the overview, since a link cannot repeat a
// POST), "old token query visit" and "bearer header" (the old ways in get the
// form and no cookie), and "hostile address" (the address goes into the
// hidden field escaped, with no script element).
func TestAuthUnauthorizedOutsideAPI(t *testing.T) {
	tests := []struct {
		name     string
		req      authReq
		wantNext string
	}{
		{"front page", authReq{target: "/"}, "/"},
		{"project page", authReq{target: "/p/app"}, "/p/app"},
		{"a page with a query", authReq{target: "/p/app/inbox?item=x"}, "/p/app/inbox?item=x"},
		{"an address that does not exist", authReq{target: "/no/such/page"}, "/no/such/page"},
		{"old token query visit", authReq{target: "/?" + oldTokenQuery("tok-4f9a1c77d2e84b0a9c3d5e6f70812345")}, "/?" + oldTokenQuery("tok-4f9a1c77d2e84b0a9c3d5e6f70812345")},
		{"bearer header", authReq{target: "/p/app", auth: bearerHeader(goodCookie())}, "/p/app"},
		{"cookie from before a password change", authReq{target: "/p/app", cookie: otherCookie()}, "/p/app"},
		{"a POST", authReq{method: "POST", target: "/p/app/pause"}, "/"},
		{"sign out without a cookie", authReq{method: "POST", target: "/signout"}, "/"},
		{"a path that only starts like the API", authReq{target: "/apiary"}, "/apiary"},
		{"hostile address", authReq{target: "/p/app?x=" + url.QueryEscape(`"><script>alert(1)</script>`)}, "/p/app?x=" + url.QueryEscape(`"><script>alert(1)</script>`)},
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
			wantSignInForm(t, rec.Body.String(), tc.wantNext)
			if strings.Contains(rec.Body.String(), "aria-invalid") {
				t.Errorf("a first visit shows a field error")
			}
			wantSecurityHeaders(t, rec)
			noSecrets(t, rec)
			if setCookie(rec) != nil {
				t.Errorf("a refused request was handed a cookie, or had one cleared")
			}
			if len(rg.paused) != 0 {
				t.Errorf("a refused request reached a handler")
			}
			if n := rg.projectCalls.Load(); n != 0 {
				t.Errorf("the sign-in form read the project list %d times", n)
			}
		})
	}
}

// TestAuthHostileAddressRaw sends a requested address holding markup that a
// request line can carry unescaped, set on the URL directly. Load-bearing:
// the hidden next field must hold it escaped, with no script element.
func TestAuthHostileAddressRaw(t *testing.T) {
	rg := newRig(t, Config{})
	req := httptest.NewRequest(http.MethodGet, "/p/app", nil)
	req.URL.RawQuery = `x="><script>alert(1)</script>`
	rec := httptest.NewRecorder()
	rg.h.ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if strings.Contains(body, "<script") || strings.Contains(body, `"><`) {
		t.Errorf("the address was drawn as markup:\n%s", body)
	}
	if !strings.Contains(body, `name="next" value="/p/app?x=&#34;&gt;&lt;script&gt;alert(1)&lt;/script&gt;"`) {
		t.Errorf("the address is not in the next field, escaped:\n%s", body)
	}
}

// TestSignIn is the table of POST /signin.
//
// Load-bearing rows:
//   - "right password": the cookie is set with every attribute, holding the
//     record's CookieValue, and the browser goes on to next.
//   - "right password, local address kept" and the "other site" rows: only a
//     local next is followed.
//   - "wrong password": 401, the field error, no cookie.
//   - "no password set" and "password file unreadable": they say what to
//     run, and never show the file or its error.
//   - "right password, stale cookie": the sign-in never consults the cookie.
func TestSignIn(t *testing.T) {
	tests := []struct {
		name         string
		form         url.Values
		cookie       string
		passErr      error
		wantStatus   int
		wantLocation string
		wantCookie   bool
		wantText     string
		wantInvalid  bool
	}{
		{name: "right password", form: signInForm(testPassword, "/p/app"), wantStatus: 303, wantLocation: "/p/app", wantCookie: true},
		{name: "right password, local address kept", form: signInForm(testPassword, "/p/x?y=1"), wantStatus: 303, wantLocation: "/p/x?y=1", wantCookie: true},
		{name: "right password, no next", form: url.Values{"password": {testPassword}}, wantStatus: 303, wantLocation: "/", wantCookie: true},
		{name: "right password, other site by double slash", form: signInForm(testPassword, "//evil.example/"), wantStatus: 303, wantLocation: "/", wantCookie: true},
		{name: "right password, other site by backslash", form: signInForm(testPassword, `/\evil.example`), wantStatus: 303, wantLocation: "/", wantCookie: true},
		{name: "right password, other site by full address", form: signInForm(testPassword, "https://evil.example/"), wantStatus: 303, wantLocation: "/", wantCookie: true},
		{name: "right password, other site by escaped slashes", form: signInForm(testPassword, "/%2F%2Fevil"), wantStatus: 303, wantLocation: "/", wantCookie: true},
		{name: "right password, address under /api", form: signInForm(testPassword, "/api/p/x"), wantStatus: 303, wantLocation: "/", wantCookie: true},
		{name: "right password, stale cookie", form: signInForm(testPassword, "/"), cookie: otherCookie(), wantStatus: 303, wantLocation: "/", wantCookie: true},
		{name: "wrong password", form: signInForm("not-the-password", "/p/app"), wantStatus: 401, wantText: signInWrong, wantInvalid: true},
		{name: "wrong password, the old one", form: signInForm("pw-ffffffffffffffffffffffffffffffff", "/"), wantStatus: 401, wantText: signInWrong, wantInvalid: true},
		{name: "empty password", form: signInForm("", "/"), wantStatus: 401, wantText: signInWrong, wantInvalid: true},
		{name: "password over 1024 bytes", form: signInForm(testPassword+strings.Repeat("x", 1024), "/"), wantStatus: 401, wantText: signInWrong, wantInvalid: true},
		{name: "no password set", form: signInForm(testPassword, "/"), passErr: webpass.ErrNoPassword, wantStatus: 401, wantText: "afk web password"},
		{name: "password file unreadable", form: signInForm(testPassword, "/"), passErr: errors.New("reading /secret/state/web-password.json: file-bytes-here"), wantStatus: 401, wantText: "could not read"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			if tc.passErr != nil {
				rg.setPassword(webpass.Record{}, tc.passErr)
			}
			rec := authReq{method: http.MethodPost, target: "/signin", form: tc.form, cookie: tc.cookie}.send(rg.h)
			body := rec.Body.String()
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d\n%s", rec.Code, tc.wantStatus, body)
			}
			wantSecurityHeaders(t, rec)
			noSecrets(t, rec)
			if loc := rec.Header().Get("Location"); loc != tc.wantLocation {
				t.Errorf("Location = %q, want %q", loc, tc.wantLocation)
			}
			c := setCookie(rec)
			if (c != nil) != tc.wantCookie {
				t.Fatalf("cookie set = %v, want %v", c != nil, tc.wantCookie)
			}
			if c != nil {
				if c.Value != goodCookie() {
					t.Errorf("cookie value = %q, want the record's CookieValue", c.Value)
				}
				if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.MaxAge != 180*24*60*60 || c.Secure {
					t.Errorf("cookie = %+v, want HttpOnly, SameSite=Lax, Path=/, 180 days and not Secure (no public address)", c)
				}
			}
			if tc.wantText != "" {
				if !strings.Contains(body, html.EscapeString(tc.wantText)) {
					t.Errorf("the form does not say %q:\n%s", tc.wantText, body)
				}
				wantSignInForm(t, body, returnTo(tc.form.Get("next")))
			}
			if got := strings.Contains(body, `aria-invalid="true" aria-describedby="signin-error"`) && strings.Contains(body, `class="field__error" id="signin-error"`); got != tc.wantInvalid {
				t.Errorf("field error shown = %v, want %v\n%s", got, tc.wantInvalid, body)
			}
			for _, leak := range []string{"file-bytes-here", "/secret/state", "not-the-password"} {
				if strings.Contains(body, leak) {
					t.Errorf("the answer repeats %q:\n%s", leak, body)
				}
			}
		})
	}
}

// TestSignInLockout: five wrong passwords in a row pause sign-in for a
// minute.
//
// Load-bearing rows: "sixth try within the minute" (429 even with the right
// password, and the record is not read, so it is not hashed either), "try
// after the minute" (the right password works again), "long password counts"
// (an over-long password cannot be used to guess without counting) and "a
// right password resets the count".
func TestSignInLockout(t *testing.T) {
	type try struct {
		advance    time.Duration
		password   string
		wantStatus int
	}
	wrong := try{password: "not-the-password", wantStatus: 401}
	five := []try{wrong, wrong, wrong, wrong, wrong}
	tests := []struct {
		name      string
		tries     []try
		wantCalls int32
	}{
		{name: "sixth try within the minute", tries: append(five[:5:5], try{advance: 59 * time.Second, password: testPassword, wantStatus: 429}), wantCalls: 5},
		{name: "try after the minute", tries: append(five[:5:5], try{advance: time.Minute, password: testPassword, wantStatus: 303}), wantCalls: 6},
		{name: "a wrong try after the minute does not lock at once", tries: append(five[:5:5], try{advance: time.Minute, password: "nope-nope", wantStatus: 401}, try{password: testPassword, wantStatus: 303}), wantCalls: 7},
		{name: "long password counts", tries: append(five[:4:4], try{password: strings.Repeat("x", 1025), wantStatus: 401}, try{password: testPassword, wantStatus: 429}), wantCalls: 4},
		{name: "a right password resets the count", tries: append(append(five[:4:4], try{password: testPassword, wantStatus: 303}), wrong, wrong, wrong, wrong, try{password: testPassword, wantStatus: 303}), wantCalls: 10},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			for i, tr := range tc.tries {
				rg.advance(tr.advance)
				rec := authReq{method: http.MethodPost, target: "/signin", form: signInForm(tr.password, "/")}.send(rg.h)
				if rec.Code != tr.wantStatus {
					t.Fatalf("try %d: status = %d, want %d\n%s", i+1, rec.Code, tr.wantStatus, rec.Body.String())
				}
				if rec.Code == http.StatusTooManyRequests {
					wantSignInForm(t, rec.Body.String(), "/")
					if !strings.Contains(rec.Body.String(), "paused for a minute after 5 wrong passwords") {
						t.Errorf("the 429 form does not say why:\n%s", rec.Body.String())
					}
					if setCookie(rec) != nil {
						t.Errorf("a locked sign-in was handed a cookie")
					}
				}
				noSecrets(t, rec)
			}
			if got := rg.passwordCalls.Load(); got != tc.wantCalls {
				t.Errorf("the password was read %d times, want %d", got, tc.wantCalls)
			}
		})
	}
}

// TestSignInParallel sends five wrong sign-ins at once, then a sixth with the
// right password. Load-bearing: the sixth gets 429, the record was read
// exactly five times, and never by two sign-ins at once. If the lockout's mutex covered only the counter,
// parallel guesses could all pass "allowed" before any was counted, and
// every one would be hashed.
func TestSignInParallel(t *testing.T) {
	rg := newRig(t, Config{})
	// The seam yields while it is "hashing", so if two sign-ins could be
	// inside it at once they would be; maxInside records the most seen.
	var inside, maxInside atomic.Int32
	rg.onPassword = func() {
		n := inside.Add(1)
		for {
			m := maxInside.Load()
			if n <= m || maxInside.CompareAndSwap(m, n) {
				break
			}
		}
		for range 100 {
			runtime.Gosched()
		}
		inside.Add(-1)
	}
	var wg sync.WaitGroup
	codes := make([]int, 5)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = authReq{method: http.MethodPost, target: "/signin", form: signInForm("not-the-password", "/")}.send(rg.h).Code
		}()
	}
	wg.Wait()
	for i, c := range codes {
		if c != http.StatusUnauthorized {
			t.Errorf("parallel try %d: status = %d, want 401", i+1, c)
		}
	}
	rec := authReq{method: http.MethodPost, target: "/signin", form: signInForm(testPassword, "/")}.send(rg.h)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("sixth try: status = %d, want 429", rec.Code)
	}
	if got := rg.passwordCalls.Load(); got != 5 {
		t.Errorf("the password was read %d times, want 5", got)
	}
	if m := maxInside.Load(); m != 1 {
		t.Errorf("%d sign-ins were checking the password at once, want 1", m)
	}
}

// TestSignInCookieSecure: the cookie is marked Secure only when the public
// address is https. The load-bearing row is plain http: a Secure cookie
// there is one the browser will not keep, which would lock the owner out.
func TestSignInCookieSecure(t *testing.T) {
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
			rec := authReq{method: http.MethodPost, target: "/signin", form: signInForm(testPassword, "/")}.send(rg.h)
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

// TestSignInChecks: the sign-in is exempt from the cookie, not from the POST
// checks. Load-bearing: "from another site" (a sign-in posted from another
// site's page is refused 403 before the password is read, so it cannot be
// used to guess or to sign a browser in to the owner's page unawares) and
// "a body over 64 KB" (413, before anything is read).
func TestSignInChecks(t *testing.T) {
	tests := []struct {
		name       string
		req        authReq
		wantStatus int
	}{
		{name: "from another site", req: authReq{method: "POST", target: "/signin", form: signInForm(testPassword, "/"), origin: "https://evil.example", host: "afk.local:4253"}, wantStatus: 403},
		{name: "Origin null", req: authReq{method: "POST", target: "/signin", form: signInForm(testPassword, "/"), origin: "null", host: "afk.local:4253"}, wantStatus: 403},
		{name: "same site", req: authReq{method: "POST", target: "/signin", form: signInForm(testPassword, "/"), origin: "http://afk.local:4253", host: "afk.local:4253"}, wantStatus: 303},
		{name: "a body over 64 KB", req: authReq{method: "POST", target: "/signin", form: signInForm(strings.Repeat("x", 70<<10), "/")}, wantStatus: 413},
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
			if tc.wantStatus != 303 {
				if setCookie(rec) != nil {
					t.Errorf("a refused sign-in was handed a cookie")
				}
				if n := rg.passwordCalls.Load(); n != 0 {
					t.Errorf("the password was read %d times for a refused sign-in", n)
				}
			}
		})
	}
}

// TestSignInPage covers GET /signin and its /api twin, which does not exist.
// Load-bearing: "GET /api/signin" (a JSON 401 and no form: the sign-in has
// no API) and "signed in" (no form for a browser that does not need it).
func TestSignInPage(t *testing.T) {
	tests := []struct {
		name         string
		req          authReq
		wantStatus   int
		wantLocation string
		wantNext     string
		wantJSON     bool
	}{
		{name: "no cookie", req: authReq{target: "/signin"}, wantStatus: 200, wantNext: "/"},
		{name: "no cookie, with a local next", req: authReq{target: "/signin?next=" + url.QueryEscape("/p/app")}, wantStatus: 200, wantNext: "/p/app"},
		{name: "no cookie, with a next to another site", req: authReq{target: "/signin?next=" + url.QueryEscape("//evil.example/")}, wantStatus: 200, wantNext: "/"},
		{name: "signed in", req: authReq{target: "/signin", cookie: goodCookie()}, wantStatus: 303, wantLocation: "/"},
		{name: "GET /api/signin", req: authReq{target: "/api/signin"}, wantStatus: 401, wantJSON: true},
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
			if loc := rec.Header().Get("Location"); loc != tc.wantLocation {
				t.Errorf("Location = %q, want %q", loc, tc.wantLocation)
			}
			switch {
			case tc.wantJSON:
				if _, ok := decode(t, rec)["error"]; !ok || strings.Contains(rec.Body.String(), "<form") {
					t.Errorf("body = %s, want a JSON error and no form", rec.Body.String())
				}
			case tc.wantStatus == 200:
				wantSignInForm(t, rec.Body.String(), tc.wantNext)
			}
			if setCookie(rec) != nil {
				t.Errorf("GET /signin handed out or cleared a cookie")
			}
		})
	}
}

// TestSignOut: POST /signout clears the cookie and goes to the overview,
// which then shows the sign-in form. Load-bearing: "signed in" (the cookie
// is cleared with the same name, path and attributes, or the browser keeps
// it), "no cookie" (the guard's 401 form, nothing cleared) and "from another
// site" (refused 403 like any POST, nothing cleared).
func TestSignOut(t *testing.T) {
	tests := []struct {
		name       string
		publicURL  string
		req        authReq
		wantStatus int
		wantClear  bool
	}{
		{name: "signed in", req: authReq{method: "POST", target: "/signout", cookie: goodCookie(), form: url.Values{}}, wantStatus: 303, wantClear: true},
		{name: "signed in, https public address", publicURL: "https://afk.example", req: authReq{method: "POST", target: "/signout", cookie: goodCookie(), form: url.Values{}}, wantStatus: 303, wantClear: true},
		{name: "no cookie", req: authReq{method: "POST", target: "/signout", form: url.Values{}}, wantStatus: 401},
		{name: "from another site", req: authReq{method: "POST", target: "/signout", cookie: goodCookie(), form: url.Values{}, origin: "https://evil.example", host: "afk.local:4253"}, wantStatus: 403},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{PublicURL: tc.publicURL})
			rec := tc.req.send(rg.h)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d\n%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			wantSecurityHeaders(t, rec)
			noSecrets(t, rec)
			c := setCookie(rec)
			if (c != nil) != tc.wantClear {
				t.Fatalf("cookie cleared = %v, want %v", c != nil, tc.wantClear)
			}
			if c == nil {
				if tc.wantStatus == 401 {
					wantSignInForm(t, rec.Body.String(), "/")
				}
				return
			}
			if loc := rec.Header().Get("Location"); loc != "/" {
				t.Errorf("Location = %q, want /", loc)
			}
			wantSecure := strings.HasPrefix(tc.publicURL, "https://")
			if c.Value != "" || c.MaxAge >= 0 || c.Path != "/" || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Secure != wantSecure {
				t.Errorf("cleared cookie = %+v, want an empty value, Max-Age 0, Path=/, HttpOnly, Lax and Secure=%v", c, wantSecure)
			}
			// The browser now holds no cookie, so the overview is the form.
			after := authReq{target: "/"}.send(rg.h)
			if after.Code != http.StatusUnauthorized {
				t.Errorf("after signing out, / = %d, want 401", after.Code)
			}
			wantSignInForm(t, after.Body.String(), "/")
		})
	}
}

// TestSignInThenOverview walks the whole way in: the form, the right
// password, its cookie on the next request. Load-bearing: the cookie a
// sign-in sets is the one the guard accepts.
func TestSignInThenOverview(t *testing.T) {
	rg := newRig(t, Config{})
	rec := authReq{method: http.MethodPost, target: "/signin", form: signInForm(testPassword, "/p/app")}.send(rg.h)
	c := setCookie(rec)
	if rec.Code != http.StatusSeeOther || c == nil {
		t.Fatalf("sign-in: %d, cookie %v", rec.Code, c)
	}
	page := authReq{target: rec.Header().Get("Location"), cookie: c.Value}.send(rg.h)
	if page.Code != http.StatusOK || strings.Contains(page.Body.String(), `action="/signin"`) {
		t.Fatalf("after signing in, %s = %d\n%s", rec.Header().Get("Location"), page.Code, page.Body.String())
	}
	// A new password signs the browser out at once.
	rg.setPassword(otherRecord, nil)
	again := authReq{target: "/p/app", cookie: c.Value}.send(rg.h)
	if again.Code != http.StatusUnauthorized {
		t.Errorf("after a password change, the old cookie got %d, want 401", again.Code)
	}
}

// TestAuthPostOrigin is the cross-site check on POST.
//
// Load-bearing rows:
//   - "another site": refused with 403 although the cookie is right, and
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
			rec := authReq{method: "POST", target: "/api/p/app/pause", cookie: goodCookie(), host: tc.host, origin: tc.origin}.send(rg.h)
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

// TestAuthPostOriginFix is the cross-site check on a fix POST (T13.7):
// starting a planning agent from another site's page must be refused before
// Deps.StartPlan is asked, on both the page and the API address.
//
// Load-bearing rows:
//   - "page fix from another site" and "api fix from another site": 403 with
//     a signed-in cookie and a complete fix body, and no seam call. The body
//     is one the seam would accept, so the refusal can only be the Origin.
//   - "page fix from the same host" and "api fix from the same host": the
//     same body is started, proving the refused rows are not refused for
//     some other reason.
func TestAuthPostOriginFix(t *testing.T) {
	fix := url.Values{"confirm": {"yes"}, "mode": {"fix"}, "text": {"The export button does nothing."}}
	tests := []struct {
		name, target, origin string
		wantStatus           int
	}{
		{"page fix from another site", "/p/app/plan/change", "https://other.example", http.StatusForbidden},
		{"api fix from another site", "/api/p/app/plan/change", "https://other.example", http.StatusForbidden},
		{"page fix from the same host", "/p/app/plan/change", "http://afk.local:4253", http.StatusSeeOther},
		{"api fix from the same host", "/api/p/app/plan/change", "http://afk.local:4253", http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRig(t, Config{})
			rg.startResult = PlanStartResult{Outcome: PlanStartStarted}
			rec := authReq{method: http.MethodPost, target: tc.target, cookie: goodCookie(), host: "afk.local:4253", origin: tc.origin, form: fix}.send(rg.h)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d\n%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			wantCalls := 0
			if tc.wantStatus != http.StatusForbidden {
				wantCalls = 1
			}
			if len(rg.startCalls) != wantCalls {
				t.Fatalf("start calls = %v, want %d", rg.startCalls, wantCalls)
			}
			if wantCalls == 1 && strings.Join(rg.startCalls[0], "|") != rg.root+"|fix|The export button does nothing." {
				t.Errorf("start call = %q, want a fix with the description", rg.startCalls[0])
			}
			if tc.wantStatus == http.StatusForbidden && !strings.Contains(rec.Body.String(), "another site") {
				t.Errorf("refusal does not say another site:\n%s", rec.Body.String())
			}
		})
	}
}

// TestAuthOriginDoesNotApplyToGET: the check is for requests that change
// something. A GET with a foreign Origin (a link followed from another site)
// must still be served, or notification links would break.
func TestAuthOriginDoesNotApplyToGET(t *testing.T) {
	rg := newRig(t, Config{})
	rec := authReq{target: "/api/", cookie: goodCookie(), origin: "https://other.example"}.send(rg.h)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

// TestNew covers what New refuses. The load-bearing row is "no Password": a
// handler built without a way to read the password could check no sign-in,
// and the only safe server in that case is none.
func TestNew(t *testing.T) {
	full := func() Deps {
		return Deps{
			LoadBuild: func(string) ([]masterplan.Task, ledger.Ledger, error) { return nil, ledger.Ledger{}, nil },
			Sessions:  func(string) []SessionView { return nil },
			Usage:     func() (usagestate.File, bool) { return usagestate.File{}, false },
			Pause:     func(string) error { return nil },
			Resume:    func(string) error { return nil },
			Password:  testPasswordSeam,
		}
	}
	tests := []struct {
		name    string
		mutate  func(*Deps)
		wantErr string
	}{
		{name: "everything given, no clock"},
		{name: "no Password", mutate: func(d *Deps) { d.Password = nil }, wantErr: "Password"},
		{name: "no LoadBuild", mutate: func(d *Deps) { d.LoadBuild = nil }, wantErr: "LoadBuild"},
		{name: "no Sessions", mutate: func(d *Deps) { d.Sessions = nil }, wantErr: "Sessions"},
		{name: "no Usage", mutate: func(d *Deps) { d.Usage = nil }, wantErr: "Usage"},
		{name: "no Pause", mutate: func(d *Deps) { d.Pause = nil }, wantErr: "Pause"},
		{name: "no Resume", mutate: func(d *Deps) { d.Resume = nil }, wantErr: "Resume"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := full()
			if tc.mutate != nil {
				tc.mutate(&d)
			}
			h, err := New(Config{}, d)
			if tc.wantErr == "" {
				if err != nil || h == nil {
					t.Fatalf("New = %v, %v; want a handler", h, err)
				}
				// With no clock given, the real one is used and a
				// request works.
				rec := authReq{target: "/api/", cookie: goodCookie()}.send(h)
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
		})
	}
}
