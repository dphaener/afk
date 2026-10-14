package web

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"net/url"
	"strings"
)

// cookieName is the cookie a `?token=` visit leaves in the browser.
const cookieName = "afk_web"

// cookieMaxAge is 180 days in seconds. The owner opens the page from a phone
// now and then; a short life would mean digging the token out of config.toml
// on a train.
const cookieMaxAge = 180 * 24 * 60 * 60

// proof says which of the three ways a request showed it knows the token.
type proof int

const (
	proofNone proof = iota
	proofCookie
	proofBearer
	proofQuery
)

// guard is the one middleware around the whole mux. Nothing is exempt: a
// request that does not prove it knows the token is answered here and never
// reaches a handler, a "not found" included.
func (s *server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w.Header())

		how := s.authorize(r)
		if how == proofNone {
			if s.isRefusedPageStyle(r) {
				next.ServeHTTP(w, r)
				return
			}
			s.unauthorized(w, r)
			return
		}

		if r.Method == http.MethodPost {
			if !s.sameOrigin(r) {
				s.fail(w, r, http.StatusForbidden, "This request came from another site and was refused. Open the afk page itself and try again from there.")
				return
			}
			// A declared length over the limit is refused before anything
			// is read. MaxBytesReader then covers a body with no declared
			// length, or one that lies about it.
			if r.ContentLength > maxBody {
				s.tooLarge(w, r)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		}

		if how == proofQuery {
			http.SetCookie(w, s.cookie())
			// The token must not stay in the address bar, the history or a
			// screenshot, so a page visit goes round once more without it.
			// The API is left alone: a script that sends ?token= expects its
			// answer, not a redirect.
			if r.Method == http.MethodGet && !isAPI(r.URL.Path) {
				w.Header().Set("Location", withoutToken(r.URL))
				w.WriteHeader(http.StatusSeeOther)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

// authorize reports how the request proved it knows the token, checking the
// cookie, then the Authorization header, then the query. A wrong value in one
// place does not spoil a right value in another: a bookmarked address with an
// old token still works for a browser that holds the cookie.
func (s *server) authorize(r *http.Request) proof {
	if c, err := r.Cookie(cookieName); err == nil && sameSecret(c.Value, s.cookieSum) {
		return proofCookie
	}
	if t, ok := bearer(r.Header.Get("Authorization")); ok && sameSecret(t, s.tokenSum) {
		return proofBearer
	}
	// Query() drops pairs it cannot parse and keeps the rest, which is the
	// wanted reading here: a damaged query is simply one without the token.
	if t := r.URL.Query().Get("token"); t != "" && sameSecret(t, s.tokenSum) {
		return proofQuery
	}
	return proofNone
}

// sameSecret compares a value from the request with a stored secret's
// SHA-256, in constant time.
//
// The candidate is hashed first because subtle.ConstantTimeCompare returns at
// once when the two lengths differ (its documentation says so), which would
// let response times reveal how long the token is. Two SHA-256 sums are
// always the same length, so the comparison takes the same time whatever was
// sent.
func sameSecret(candidate string, want [sha256.Size]byte) bool {
	got := sha256.Sum256([]byte(candidate))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}

// bearer returns the token from an `Authorization: Bearer <token>` header.
// The scheme word is matched without regard to case, as HTTP asks.
func bearer(header string) (string, bool) {
	scheme, token, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}

// cookie builds the cookie that stands in for the token from then on.
//
// SameSite is Lax and not Strict on purpose. The owner's usual way in is a
// tap on a notification, which opens the item's address from another app or
// site: a cross-site, top-level GET. A Strict cookie is not sent on that
// request, so every tap would land on the "open the page with ?token=" page.
// Lax sends the cookie on that GET and still withholds it from a cross-site
// POST, and POST is the only method that changes anything here.
//
// Secure is set only when the public address is https. The page itself speaks
// plain HTTP (it sits behind Tailscale or a reverse proxy), and a browser
// refuses to store a Secure cookie that arrived over plain HTTP, which would
// lock out anyone using it straight over the local network.
func (s *server) cookie() *http.Cookie {
	return &http.Cookie{
		Name:     cookieName,
		Value:    s.cookieValue,
		Path:     "/",
		MaxAge:   cookieMaxAge,
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
	}
}

// sameOrigin decides whether a POST may go ahead. A browser adds an Origin
// header to a form posted from a page, so a form on some other site that
// targets this one arrives with that site's origin and is refused. A request
// with no Origin (curl, a script, an old browser) is allowed: it still had to
// prove the token, and the Lax cookie is not sent on a cross-site POST anyway.
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

// withoutToken is the address to send a `?token=` visit on to: the same path
// and query with every `token` value taken out.
//
// The path is rebuilt, not copied. This middleware runs before the mux cleans
// the path, so a request for `//other.example/?token=…` or `/\other.example`
// would otherwise be echoed into the Location header, and a browser reads
// both as "go to other.example". Collapsing the leading slashes to one and
// letting net/url escape the rest keeps the redirect on this site whatever
// was asked for.
func withoutToken(u *url.URL) string {
	q := u.Query()
	q.Del("token")
	target := url.URL{Path: "/" + strings.TrimLeft(u.Path, `/\`), RawQuery: q.Encode()}
	return target.String()
}

// setSecurityHeaders puts the same headers on every answer, refusals
// included.
//
//   - no-store: the pages show live state and an answer form; a cached copy
//     shown later would be wrong, and a shared cache must never hold one.
//   - same-origin referrer: an address here may still carry ?token= for one
//     request, and must not be passed to any site a link leads to, so no
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
//     page.css) and inline, and forms that post back here. Nothing else: no
//     inline script, image, font or outside address. No page uses an inline
//     style any more (TestPageTemplatesHaveNoInlineScript keeps it so), but
//     'unsafe-inline' stays for now: taking it out is its own change, to be
//     checked against everything the plan renderer can emit.
func setSecurityHeaders(h http.Header) {
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; connect-src 'self'; style-src 'self' 'unsafe-inline'; form-action 'self'")
}

// unauthorized answers a request that did not prove the token: JSON under
// /api, and elsewhere a small page that says what to do. Neither repeats
// anything the request sent.
func (s *server) unauthorized(w http.ResponseWriter, r *http.Request) {
	if isAPI(r.URL.Path) {
		writeJSON(w, http.StatusUnauthorized, ErrorView{
			Status: http.StatusUnauthorized,
			Error:  "This request needs the token. Send it as the header \"Authorization: Bearer\" followed by the token from [web] in config.toml.",
		})
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write(s.refusedPage)
}

// tooLarge answers a POST whose body is over the limit.
func (s *server) tooLarge(w http.ResponseWriter, r *http.Request) {
	s.fail(w, r, http.StatusRequestEntityTooLarge, "The request was larger than 64 KB and was refused. Send a shorter answer.")
}

// refusedStyles are the static files the refusal page links: the design
// system's sheet and the page's own. They are the one thing served without
// the token (see isRefusedPageStyle).
var refusedStyles = []string{"afk.css", "page.css"}

// isRefusedPageStyle reports whether a request without the token asks for one
// of the refusal page's stylesheets at its current address.
//
// Everything else answers a request without the token with the refusal, and
// that cannot hold for these two: the refusal page is exactly what such a
// browser shows, so stylesheets behind the token would never load on it and
// the page would fall back to the browser's bare look. What is let through
// gives a stranger nothing: two stylesheets that ship in the open-source
// binary, at the one address the refusal page has already shown them, with
// no project data in either. Every other static address, a stale hash of
// these two included, still gets the refusal, so 401 against 404 still maps
// nothing.
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

// refusedPageFor builds the page that refuses a request without the token,
// given the addresses of its two stylesheets. New calls it once, so the page
// is fixed text from then on with nothing from any request filled in: it
// cannot echo a token or an address back. It is not a template because the
// page templates are drawn with the bars, whose data (the project count) is
// exactly what a stranger must not see.
//
// There is no Sign in button although the design's sign-in screen has one:
// afk has no sign-in, only the token in the address, so the page says how to
// use that instead.
func refusedPageFor(afkCSS, pageCSS string) string {
	return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>This page is private - afk</title>
<link rel="stylesheet" href="` + afkCSS + `">
<link rel="stylesheet" href="` + pageCSS + `">
</head>
<body class="afk">
<div class="shell">
<main class="page page--alone">
<p class="wordmark">afk</p>
<h1>This page is private</h1>
<p>Open this page once with <code>?token=</code> at the end of the address, followed by the token from the <code>[web]</code> section of afk's <code>config.toml</code>. Your browser will remember it after that.</p>
<p class="meta">If you have done that before, the token has probably been changed. Open the page with the new one.</p>
</main>
</div>
</body>
</html>
`
}
