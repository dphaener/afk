package web

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/dphaener/afk/internal/webpass"
)

// cookieName is the cookie a right password leaves in the browser.
const cookieName = "afk_web"

// cookieMaxAge is 180 days in seconds. The owner opens the page from a phone
// now and then; a short life would mean typing the password on a train.
const cookieMaxAge = 180 * 24 * 60 * 60

// The sign-in lockout's numbers: this many wrong passwords in a row pause
// sign-in for lockoutFor. Five is enough for a typo or two and a caps-lock
// key; a minute is short enough that the owner simply waits, and long enough
// that a guesser gets at most five guesses a minute.
const (
	lockoutAfter = 5
	lockoutFor   = time.Minute
)

// maxPasswordBytes is the longest password a sign-in will hash. PBKDF2's
// cost grows with the password only a little, but a megabyte hashed 600,000
// times is still work a stranger could make the machine do for free; a
// longer password is treated as wrong without hashing (and counts as a
// wrong try, so it cannot be used to get round the lockout).
const maxPasswordBytes = 1024

// The sign-in addresses. They are registered straight on the mux, never
// through routes, because routes also registers every entry under /api and
// a sign-in has no JSON form.
const (
	signInPath  = "/signin"
	signOutPath = "/signout"
)

// guard is the one middleware around the whole mux. Nothing is exempt but
// the sign-in itself and a few exact files that hold no data (the sign-in
// form's stylesheets and the app files; see isRefusedPageStyle and
// isPublicAppFile): a request that does not hold a good cookie is answered
// here with the sign-in form and never reaches a handler, a "not found"
// included, so a stranger cannot map the site by telling 401 from 404.
func (s *server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w.Header())

		// The sign-in form and its POST are the way to get a cookie, so they
		// cannot ask for one. The POST still passes the Origin check and the
		// size limit like any other: a sign-in posted from another site is
		// exactly the request those exist to refuse.
		if r.URL.Path == signInPath {
			switch r.Method {
			case http.MethodGet, http.MethodHead:
				next.ServeHTTP(w, r)
				return
			case http.MethodPost:
				if !s.checkPost(w, r) {
					return
				}
				next.ServeHTTP(w, r)
				return
			}
		}

		if !s.signedInRequest(r) {
			if s.isRefusedPageStyle(r) || s.isPublicAppFile(r) {
				next.ServeHTTP(w, r)
				return
			}
			s.unauthorized(w, r)
			return
		}

		if r.Method == http.MethodPost && !s.checkPost(w, r) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

// checkPost runs the checks every POST passes before its handler: the Origin
// check and the size limit. It answers and returns false when the request is
// refused.
func (s *server) checkPost(w http.ResponseWriter, r *http.Request) bool {
	if !s.sameOrigin(r) {
		s.fail(w, r, http.StatusForbidden, "This request came from another site and was refused. Open the afk page itself and try again from there.")
		return false
	}
	// A declared length over the limit is refused before anything is read.
	// MaxBytesReader then covers a body with no declared length, or one that
	// lies about it.
	if r.ContentLength > maxBody {
		s.tooLarge(w, r)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	return true
}

// signedInRequest reads the password record again and checks the request's
// cookie against it. The record is read on every request, not once at
// start-up, so a password saved by `afk web password` while the daemon runs
// signs every old browser out at once.
func (s *server) signedInRequest(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	rec, err := s.d.Password()
	return signedIn(c.Value, rec, err)
}

// signedIn decides whether a cookie value proves a sign-in under the stored
// record. With no record (none saved, or one that cannot be read) nobody is
// signed in. An empty value on either side matches nothing: an invalid
// record's CookieValue is "", and an empty cookie must never pass as equal
// to it.
func signedIn(cookie string, rec webpass.Record, err error) bool {
	if err != nil || cookie == "" {
		return false
	}
	want := rec.CookieValue()
	if want == "" {
		return false
	}
	return sameSecret(cookie, sha256.Sum256([]byte(want)))
}

// sameSecret compares a value from the request with a stored secret's
// SHA-256, in constant time.
//
// The candidate is hashed first because subtle.ConstantTimeCompare returns at
// once when the two lengths differ (its documentation says so), which would
// let response times reveal how long the secret is. Two SHA-256 sums are
// always the same length, so the comparison takes the same time whatever was
// sent.
func sameSecret(candidate string, want [sha256.Size]byte) bool {
	got := sha256.Sum256([]byte(candidate))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}

// lockout counts wrong passwords for the whole page. There is one owner and
// one password, so one counter is the right size: a guesser cannot spread
// guesses over "accounts" that do not exist.
//
// Its methods do not lock. The caller holds mu across the whole sign-in
// attempt, from allowed through reading the record and the slow PBKDF2 check
// to failed or succeeded. That is on purpose although it makes sign-ins wait
// for one another: if the lock covered only the counter, a burst of parallel
// guesses would all pass allowed before the first was counted, and every one
// of them would be hashed. Held across the hash, guesses are taken one at a
// time, the sixth sees the lock, and the lockout bounds both the guesses and
// the CPU they cost. The owner signs in rarely, so the wait is never felt.
type lockout struct {
	mu    sync.Mutex
	fails int
	until time.Time
}

// allowed reports whether a sign-in may be tried at now. Once a lock's
// minute has passed the count starts again from nothing.
func (l *lockout) allowed(now time.Time) bool {
	if l.until.IsZero() {
		return true
	}
	if now.Before(l.until) {
		return false
	}
	l.fails, l.until = 0, time.Time{}
	return true
}

// failed counts one wrong password; the fifth in a row locks sign-in for a
// minute from now.
func (l *lockout) failed(now time.Time) {
	l.fails++
	if l.fails >= lockoutAfter {
		l.until = now.Add(lockoutFor)
	}
}

// succeeded clears the count: only wrong passwords in a row lock.
func (l *lockout) succeeded() {
	l.fails, l.until = 0, time.Time{}
}

// returnTo is the address a sign-in sends the browser on to, from the form's
// hidden next field, which a stranger can fill with anything.
//
// Only a path on this site is followed: it must start with exactly one "/",
// and parse with no scheme and no host. Anything else becomes "/". The
// checks are made on the parsed, decoded path as well as the raw text,
// because a browser reads "//other.example" and "/\other.example" as another
// site, and "/%2F%2Fother.example" decodes to the first. The path is then
// rebuilt and escaped by net/url, so what reaches the Location header is
// never the raw text. The sign-in and sign-out addresses, and anything under
// /api (which answers JSON a browser should never land on), also become "/".
func returnTo(raw string) string {
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.HasPrefix(raw, `/\`) {
		return "/"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" {
		return "/"
	}
	p := u.Path
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, `/\`) {
		return "/"
	}
	switch clean := path.Clean(p); {
	case clean == signInPath, clean == signOutPath, isAPI(clean):
		return "/"
	}
	return (&url.URL{Path: p, RawQuery: u.RawQuery}).String()
}

// cookie builds the cookie a right password earns, holding value.
//
// SameSite is Lax and not Strict on purpose. The owner's usual way in is a
// tap on a notification, which opens the item's address from another app or
// site: a cross-site, top-level GET. A Strict cookie is not sent on that
// request, so every tap would land on the sign-in form. Lax sends the cookie
// on that GET and still withholds it from a cross-site POST, and POST is the
// only method that changes anything here.
//
// Secure is set only when the public address is https. The page itself speaks
// plain HTTP (it sits behind Tailscale or a reverse proxy), and a browser
// refuses to store a Secure cookie that arrived over plain HTTP, which would
// lock out anyone using it straight over the local network.
func (s *server) cookie(value string) *http.Cookie {
	return &http.Cookie{
		Name:     cookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   cookieMaxAge,
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
	}
}

// SignInView is the sign-in form. It has no JSON form: under /api a request
// without a cookie gets an ErrorView instead.
type SignInView struct {
	// Next is the address to go on to after signing in, raw as asked for;
	// returnTo decides on submit whether it is followed.
	Next string
	// FieldError is the sentence under the password field after a wrong
	// password, or "".
	FieldError string
	// Notice is a sentence about sign-in as a whole (paused after wrong
	// passwords, no password set, the file cannot be read), or "".
	Notice string
}

// The sign-in form's sentences. None of them repeats anything the request
// sent or anything from the password file.
const (
	signInWrong      = "That password is not right. Try again."
	signInLocked     = "Sign-in is paused for a minute after 5 wrong passwords. Wait a minute, then try again."
	signInNoPassword = "No password is set for this page yet. On the machine afk runs on, run afk web password in a terminal, then sign in here."
	signInUnreadable = "afk could not read the page's password file. On the machine afk runs on, run afk web password again in a terminal to set a new password."
)

// signInForm draws the sign-in form at status.
func (s *server) signInForm(w http.ResponseWriter, r *http.Request, status int, v SignInView) {
	s.renderPage(w, r, status, "signin", v)
}

// signInPage is GET /signin: the form, or the overview for a browser that is
// already signed in.
func (s *server) signInPage(w http.ResponseWriter, r *http.Request) {
	if s.signedInRequest(r) {
		w.Header().Set("Location", "/")
		w.WriteHeader(http.StatusSeeOther)
		return
	}
	s.signInForm(w, r, http.StatusOK, SignInView{Next: returnTo(r.URL.Query().Get("next"))})
}

// signIn is POST /signin. It never looks at the cookie: a browser holding a
// stale one must still be able to sign in.
func (s *server) signIn(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			s.tooLarge(w, r)
			return
		}
		s.signInForm(w, r, http.StatusBadRequest, SignInView{Next: "/", FieldError: signInWrong})
		return
	}
	next := returnTo(r.PostForm.Get("next"))
	password := r.PostForm.Get("password")

	s.lock.mu.Lock()
	defer s.lock.mu.Unlock()
	now := s.d.Now()
	if !s.lock.allowed(now) {
		s.signInForm(w, r, http.StatusTooManyRequests, SignInView{Next: next, Notice: signInLocked})
		return
	}
	if len(password) > maxPasswordBytes {
		s.lock.failed(now)
		s.signInForm(w, r, http.StatusUnauthorized, SignInView{Next: next, FieldError: signInWrong})
		return
	}
	rec, err := s.d.Password()
	switch {
	case errors.Is(err, webpass.ErrNoPassword):
		s.signInForm(w, r, http.StatusUnauthorized, SignInView{Next: next, Notice: signInNoPassword})
		return
	case err != nil:
		// The error names the file's path and may carry an OS error; neither
		// is shown to a browser that has not signed in.
		s.signInForm(w, r, http.StatusUnauthorized, SignInView{Next: next, Notice: signInUnreadable})
		return
	}
	value := rec.CookieValue()
	if value == "" || !rec.Verify(password) {
		s.lock.failed(now)
		s.signInForm(w, r, http.StatusUnauthorized, SignInView{Next: next, FieldError: signInWrong})
		return
	}
	s.lock.succeeded()
	http.SetCookie(w, s.cookie(value))
	w.Header().Set("Location", next)
	w.WriteHeader(http.StatusSeeOther)
}

// signOut is POST /signout. The guard has already checked the cookie and the
// Origin. The cookie is cleared with the same name, path and attributes it
// was set with, or the browser would keep it. It signs out this browser
// only: every browser signed in with one password holds the same value, so
// signing all of them out means setting a new password.
func (s *server) signOut(w http.ResponseWriter, r *http.Request) {
	c := s.cookie("")
	c.MaxAge = -1
	http.SetCookie(w, c)
	w.Header().Set("Location", "/")
	w.WriteHeader(http.StatusSeeOther)
}

// sameOrigin decides whether a POST may go ahead. A browser adds an Origin
// header to a form posted from a page, so a form on some other site that
// targets this one arrives with that site's origin and is refused. A request
// with no Origin (curl, a script, an old browser) is allowed: it still had to
// hold a good cookie, or for a sign-in know the password, and the Lax cookie
// is not sent on a cross-site POST anyway.
//
// Besides the request's own Host, the origin of the configured public address
// is accepted. Behind a reverse proxy that does not pass the Host header on,
// Host is the proxy's upstream address (127.0.0.1:4253, say) while the
// browser's Origin is the public one; comparing with Host alone would refuse
// every button press made through such a proxy. The public address is the
// owner's own setting, so trusting it gives nothing to another site.
func (s *server) sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		// Includes the literal "null" a browser sends from a sandboxed or
		// file page: it names no host, so it cannot be this one.
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	return s.publicOrigin != "" && strings.ToLower(u.Scheme+"://"+u.Host) == s.publicOrigin
}

// setSecurityHeaders puts the same headers on every answer, refusals
// included.
//
//   - no-store: the pages show live state and an answer form; a cached copy
//     shown later would be wrong, and a shared cache must never hold one.
//   - same-origin referrer: an address here names projects, sessions and
//     inbox items, and must not be passed to any site a link leads to, so no
//     referrer leaves this site. It is not "no-referrer", although that reads
//     as the safer choice: the Fetch standard ("append a request `Origin`
//     header") has a browser send `Origin: null` on every POST made under
//     no-referrer, this page's own forms included, and sameOrigin then
//     refuses each button press as coming from another site. Under
//     same-origin a form on this page posts with its real origin.
//   - nosniff and DENY: the content is what the type says, and the page is
//     never to be shown inside another site's frame, where a button could be
//     pressed by trickery.
//   - The content policy allows scripts and connections from this site only
//     (live.js and the /events stream), styles from this site (afk.css and
//     page.css) and inline, images from this site (the app icons the head
//     links as favicons), the app manifest and the service worker from this
//     site, and forms that post back here. Nothing else: no inline script,
//     font or outside address. Without img-src and manifest-src the
//     default-src 'none' would refuse the favicon and the manifest, and
//     without worker-src the worker's registration. No page uses an inline
//     style any more (TestPageTemplatesHaveNoInlineScript keeps it so), but
//     'unsafe-inline' stays for now: taking it out is its own change, to be
//     checked against everything the plan renderer can emit.
func setSecurityHeaders(h http.Header) {
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; connect-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self'; manifest-src 'self'; worker-src 'self'; form-action 'self'")
}

// unauthorized answers a request without a good cookie: JSON under /api, and
// elsewhere the sign-in form at the address asked for, so that signing in
// goes straight on to it. A GET's address is kept in the form's next field
// (escaped by the template, and checked by returnTo on submit); any other
// method goes on to the overview, because a link cannot repeat a POST.
func (s *server) unauthorized(w http.ResponseWriter, r *http.Request) {
	if isAPI(r.URL.Path) {
		writeJSON(w, http.StatusUnauthorized, ErrorView{
			Status: http.StatusUnauthorized,
			Error:  "This request needs you to be signed in. Open the afk web page in a browser and sign in with its password.",
		})
		return
	}
	next := "/"
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		next = r.URL.RequestURI()
	}
	s.signInForm(w, r, http.StatusUnauthorized, SignInView{Next: next})
}

// tooLarge answers a POST whose body is over the limit.
func (s *server) tooLarge(w http.ResponseWriter, r *http.Request) {
	s.fail(w, r, http.StatusRequestEntityTooLarge, "The request was larger than 64 KB and was refused. Send a shorter answer.")
}

// refusedStyles are the static files the sign-in form links: the design
// system's sheet and the page's own. They are the one thing served without a
// cookie (see isRefusedPageStyle).
var refusedStyles = []string{"afk.css", "page.css"}

// isRefusedPageStyle reports whether a request without a cookie asks for one
// of the sign-in form's stylesheets at its current address.
//
// Everything else answers a request without a cookie with the sign-in form,
// and that cannot hold for these two: the form is exactly what such a
// browser shows, so stylesheets behind the cookie would never load on it and
// the form would fall back to the browser's bare look. What is let through
// gives a stranger nothing: two stylesheets that ship in the open-source
// binary, at the one address the form has already shown them, with no
// project data in either. Every other static address, a stale hash of these
// two included, still gets the form, so 401 against 404 still maps nothing.
func (s *server) isRefusedPageStyle(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	for _, name := range refusedStyles {
		if _, ok := s.assets[name]; ok && r.URL.Path == s.assetURL(name) {
			return true
		}
	}
	return false
}

// isPublicAppFile reports whether a request without a cookie asks for one of
// the files that make the page an app: the manifest, the service worker, or
// an icon in static/icons/ at its current address.
//
// A browser fetches each of these without the cookie: the manifest is
// fetched with no credentials unless the page asks otherwise, a phone reads
// the home-screen icon on its own, and the sign-in form (the page a browser
// without the cookie shows) links the favicon. Behind the sign-in the page
// could never be installed. What is let through gives a stranger nothing:
// files that ship in the open-source binary, with no project data in any of
// them. Only GET and HEAD pass, and only these exact addresses: no prefix
// match, so an icon under a stale or made-up hash, any other static file and
// any page still get the sign-in form, and 401 against 404 still maps
// nothing.
func (s *server) isPublicAppFile(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	switch r.URL.Path {
	case manifestPath, serviceWorkerPath:
		return true
	}
	for name := range s.assets {
		if strings.HasPrefix(name, "icons/") && r.URL.Path == s.assetURL(name) {
			return true
		}
	}
	return false
}
