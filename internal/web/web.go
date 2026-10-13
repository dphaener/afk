// Package web is the page `afk daemon` serves so the owner can see what afk
// is doing, and unblock it, from a phone: each project's current task, the
// usage reading, the inbox with a way to answer, and pause and resume. Until
// it existed the only way in was a terminal on the machine itself.
//
// Three rules hold for everything in the package.
//
// Every route needs the token. One middleware wraps the whole mux and nothing
// is exempt, not even a "not found" answer, because the page can answer inbox
// questions and pause a build: anyone who can reach the port without the
// token must learn nothing and change nothing. New refuses an empty token for
// the same reason, so a server with no secret cannot be built by mistake.
//
// Handlers read the files fresh on every request and keep no cache. The
// daemon, the sessions and the `afk` commands rewrite those files all the
// time, and the server shares no memory with the build loop, so a cached
// answer would show a phase or an inbox that is no longer true. The files are
// small and local; reading them again is cheaper than being wrong.
//
// Handlers never write the ledger and never act for the build loop. An answer
// is stored in the inbox and nothing else happens here: the loop sees the
// answered item on its next pass and un-parks the task itself, exactly as it
// does after `afk inbox answer`. Two writers deciding what a task does next
// would race; one writer with a slightly later reaction does not.
//
// The package starts no listener and logs nothing per request. Opening the
// port, and shutting it, is the caller's job.
package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/usagestate"
)

// Project is one project the page shows. Name is what appears in addresses
// (/p/{name}); Root is its main checkout, where .afk/build and .afk/inbox
// live.
type Project struct {
	Name string
	Root string
}

// Config is the part of the machine's [web] settings the handlers need. The
// bind address is not here: this package never opens a port.
type Config struct {
	// Token is the secret every request must prove it knows. Never empty in
	// a built server (New refuses it).
	Token string
	// PublicURL is the address the owner's browser uses for the page, or
	// empty. It decides two things: whether the cookie is marked Secure, and
	// which other origin a form may be posted from (see sameOrigin).
	PublicURL string
}

// SessionView is what the summary needs to know about one session. It is a
// plain copy so this package does not depend on how sessions are discovered.
type SessionView struct {
	Name string
	// Phase is the phase from the session's status.json. It means nothing
	// when Stale is true.
	Phase         string
	Stale         bool
	AwaitingInput bool
	HostRunning   bool
}

// Deps is everything the handlers reach outside the package for, as
// functions, so tests can supply all of it. The inbox, the pause flag, the
// stop marker and daemon.json are not here: they are read straight from the
// project folder with the plain file readers of internal/inbox and
// internal/daemonstate, and a test points them at a temp folder.
type Deps struct {
	// Projects lists the configured projects. It is a function, called on
	// every request, because the list can change while the daemon runs (a
	// project is added by `afk project add` or by approving a plan) and a
	// list copied at start-up would never show it. Nil means none.
	Projects func() []Project
	// Reviews lists the plans waiting for approval whose project is not a
	// configured one yet. Only the plan view is served for them: there is no
	// build to show, pause or answer for. Nil means none.
	Reviews func() []Project
	// ReadPlan reads the master plan of the project at root and says where
	// it is. An error is shown on the plan view as the page's problem; it
	// does not fail the request. Nil means this page cannot show plans, and
	// the plan view says so.
	ReadPlan func(root string) (path string, data []byte, err error)
	// LoadBuild reads a project's master plan and ledger. An error is shown
	// as the project's problem; it does not fail the request.
	LoadBuild func(root string) ([]masterplan.Task, ledger.Ledger, error)
	// Sessions lists a project's sessions. It must not block (no dialing).
	Sessions func(root string) []SessionView
	// Usage returns the last stored usage reading, and false when there is
	// none.
	Usage func() (usagestate.File, bool)
	// Pause and Resume do what `afk pause` and `afk resume` do for the
	// project at root, without printing.
	Pause  func(root string) error
	Resume func(root string) error
	// Now is the clock. Nil means time.Now.
	Now func() time.Time
}

// maxBody is the most a POST may carry. An answer is a sentence or a pasted
// value; 64 KB is far more than that needs and small enough that a flood of
// requests cannot fill memory.
const maxBody = 64 << 10

// server holds what New was given plus values worked out once from the
// token. Nothing in it changes after New, so requests share it safely, and
// none of it is read from a project's files: that is the "no cache" rule.
type server struct {
	cfg Config
	d   Deps
	// tokenSum is the SHA-256 of the token and cookieSum the SHA-256 of the
	// value the cookie must hold. A candidate is hashed and compared with
	// these (see sameSecret).
	tokenSum  [sha256.Size]byte
	cookieSum [sha256.Size]byte
	// cookieValue is what a `?token=` visit stores in the browser.
	cookieValue string
	// publicOrigin is the scheme and host of cfg.PublicURL, lower-cased, or
	// empty.
	publicOrigin string
	secure       bool
	// pages holds one parsed template set per page name, built once in New.
	pages map[string]*template.Template
}

// route is one address and the handler method behind it. The path carries no
// /api prefix: the same method serves the JSON API now and the page later,
// so register adds the prefix and the handlers never mention it.
type route struct {
	method string
	path   string
	handle func(*server, http.ResponseWriter, *http.Request)
}

// routes is every address the server answers. Tests walk it to prove that
// each one refuses a request without the token, so a route added here is
// covered by that test without anyone remembering to add a row.
var routes = []route{
	{http.MethodGet, "/{$}", (*server).overview},
	{http.MethodGet, "/usage", (*server).usage},
	{http.MethodGet, "/p/{project}", (*server).project},
	// The plan view is GET only, and that is deliberate: a plan is approved
	// with `afk plan approve` in the project folder and never from here.
	{http.MethodGet, "/p/{project}/plan", (*server).plan},
	{http.MethodGet, "/p/{project}/inbox", (*server).inboxList},
	{http.MethodGet, "/p/{project}/inbox/{id}", (*server).item},
	{http.MethodPost, "/p/{project}/inbox/{id}/answer", (*server).answer},
	{http.MethodPost, "/p/{project}/pause", (*server).pause},
	{http.MethodPost, "/p/{project}/resume", (*server).resume},
}

// New builds the handler for the page. It returns an error when the token is
// empty or a dependency is missing, so the caller finds out at start-up and
// not on the first request.
func New(cfg Config, d Deps) (http.Handler, error) {
	if cfg.Token == "" {
		return nil, errors.New("the web page needs a token; set [web] token in config.toml")
	}
	switch {
	case d.LoadBuild == nil:
		return nil, errors.New("the web page was given no way to read the plan and ledger (LoadBuild)")
	case d.Sessions == nil:
		return nil, errors.New("the web page was given no way to list sessions (Sessions)")
	case d.Usage == nil:
		return nil, errors.New("the web page was given no way to read usage (Usage)")
	case d.Pause == nil, d.Resume == nil:
		return nil, errors.New("the web page was given no way to pause or resume a project (Pause, Resume)")
	}
	if d.Now == nil {
		d.Now = time.Now
	}

	s := &server{cfg: cfg, d: d}
	s.cookieValue = cookieValueFor(cfg.Token)
	s.tokenSum = sha256.Sum256([]byte(cfg.Token))
	s.cookieSum = sha256.Sum256([]byte(s.cookieValue))
	s.secure = strings.HasPrefix(cfg.PublicURL, "https://")
	if u, err := url.Parse(cfg.PublicURL); err == nil && u.Scheme != "" && u.Host != "" {
		s.publicOrigin = strings.ToLower(u.Scheme + "://" + u.Host)
	}

	pages, err := parsePages()
	if err != nil {
		return nil, err
	}
	s.pages = pages

	mux := http.NewServeMux()
	s.register(mux, "/api")
	s.register(mux, "")
	// Everything else, including a known address asked for with the wrong
	// method, gets the same plain sentence as any other error instead of the
	// standard library's bare "404 page not found".
	mux.HandleFunc("/", s.notFound)
	return s.guard(mux), nil
}

// register adds every route under prefix ("/api" for the JSON API).
func (s *server) register(mux *http.ServeMux, prefix string) {
	for _, rt := range routes {
		mux.HandleFunc(rt.method+" "+prefix+rt.path, func(w http.ResponseWriter, r *http.Request) {
			rt.handle(s, w, r)
		})
	}
}

// cookieValueFor is what the cookie holds for a token: the lowercase hex
// SHA-256 of "afk-web:" and the token.
//
// It is a hash and not the token because a cookie is the easiest thing to
// lose: it sits in the browser's store, is sent on every request and shows
// up in a proxy's logs. Whoever gets hold of the hash can use the page until
// the token is changed, but cannot read the token out of it, and the token
// is also what `Authorization: Bearer` and `?token=` accept. The fixed prefix
// keeps the value from being the plain SHA-256 of the token, which a person
// might also have computed and stored somewhere else.
func cookieValueFor(token string) string {
	sum := sha256.Sum256([]byte("afk-web:" + token))
	return hex.EncodeToString(sum[:])
}

// isAPI reports whether the path is under /api, where every answer is JSON.
func isAPI(path string) bool {
	return path == "/api" || strings.HasPrefix(path, "/api/")
}

// respond is the one place a handler's answer is written. name says which
// view this is ("overview", "usage", "project", "plan", "inbox", "item",
// "error").
// A path under /api gets the view as JSON. Any other path gets the page
// named by name, except that a successful POST redirects (see redirectAfter)
// so that reloading the page they land on does not send the form again.
func (s *server) respond(w http.ResponseWriter, r *http.Request, status int, name string, view any) {
	if isAPI(r.URL.Path) {
		writeJSON(w, status, view)
		return
	}
	if loc := redirectAfter(r, status, name, view); loc != "" {
		w.Header().Set("Location", loc)
		w.WriteHeader(http.StatusSeeOther)
		return
	}
	s.renderPage(w, status, name, view)
}

// fail answers with an error in plain language.
func (s *server) fail(w http.ResponseWriter, r *http.Request, status int, message string) {
	s.respond(w, r, status, "error", ErrorView{Status: status, Error: message})
}

// notFound is the catch-all route.
func (s *server) notFound(w http.ResponseWriter, r *http.Request) {
	s.fail(w, r, http.StatusNotFound, "There is nothing at this address, or it does not take this kind of request. Go back to the overview and start from there.")
}

// writeJSON encodes the whole value before writing anything, so a value that
// cannot be encoded becomes a clean 500 and not half an object after a 200.
func writeJSON(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(v); err != nil {
		status = http.StatusInternalServerError
		buf.Reset()
		buf.WriteString(`{"error":"The answer could not be written as JSON. This is a fault in afk; please report it."}` + "\n")
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// ErrorView is the body of every error answer. Item is set only when the
// error is about an item whose stored state the caller should see (an answer
// that came second).
type ErrorView struct {
	// Status is the HTTP status, kept for the error page; the JSON body does
	// not repeat it.
	Status int         `json:"-"`
	Error  string      `json:"error"`
	Item   *inbox.Item `json:"item,omitempty"`
}
