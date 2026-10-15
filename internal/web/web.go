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
// token must learn nothing and change nothing. The one thing served without
// it is the two stylesheets the refusal page itself links (see
// isRefusedPageStyle in auth.go), which hold nothing but looks. New refuses an empty token for
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
	"github.com/dphaener/afk/internal/planchat"
	"github.com/dphaener/afk/internal/projectrows"
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

// SessionView is what the page needs to know about one session: the
// summary's few facts and the Sessions screens' overview. It is a plain copy
// so this package does not depend on how sessions are discovered; nothing in
// it is a type of internal/discover, internal/state or internal/host, and the
// caller fills it from discovery's row.
//
// The whole list is also the `sessions` live part's fingerprint (live.go
// hashes Deps.Sessions' answer), so every field here is one that changes only
// when something the owner would want to see changes. That is why host.json's
// last_output_at is deliberately absent, and must stay absent: the host
// rewrites it as the agent prints, so a field holding it would differ on
// nearly every two-second tick and every open page would refetch itself for
// nothing. Wedged is the bounded form of the same fact (set once, cleared
// once).
type SessionView struct {
	Name string
	// Phase is the phase from the session's status.json. It means nothing
	// when Stale is true.
	Phase string
	// Stale is true when Phase means nothing: status.json is unreadable, or
	// the session is a one-off that has none (StatusStale or Scratch). The
	// project page relies on that folding; the Sessions screens use the two
	// fields below to tell the cases apart.
	Stale         bool
	AwaitingInput bool
	// HostRunning is advisory: discovery's signal-0 check, which a recycled
	// process id can fool.
	HostRunning bool

	// Label is the session's display label, or "" when none is set.
	Label string
	// Branch and BaseBranch are status.json's branch and base_branch.
	Branch, BaseBranch string
	// SessionID is status.json's session_id, or "".
	SessionID  string
	Autonomous bool
	// StatusStale is true when status.json is there but could not be read
	// as a valid file, and StaleReason says why. A one-off is not stale.
	StatusStale bool
	StaleReason string
	// Scratch marks a one-off session: a host and no status.json.
	Scratch bool
	// Planning marks the planning session of the project's master plan.
	Planning bool
	// Problem is a worktree problem row's reason in plain words, and empty
	// for every row that is a session.
	Problem   string
	LoopCount int
	// UpdatedAt is status.json's updated_at; zero when unknown.
	UpdatedAt time.Time
	// StartedAt is host.json's started_at; zero when unknown.
	StartedAt    time.Time
	WorktreePath string
	// PlanTotal, PlanDone and PlanFailed count the session's work-plan
	// tasks; PlanError is why the work plan could not be read, or "".
	PlanTotal, PlanDone, PlanFailed int
	PlanError                       string
	// Verdict is "APPROVE", "REJECT" or "" when the session has not been
	// reviewed. VerdictChecks are the verdict file's checks in its order;
	// VerdictError is why the verdict file could not be read, or "".
	Verdict       string
	VerdictChecks []SessionCheck
	VerdictError  string
	// Wedged is true while the host has marked the agent as gone quiet.
	Wedged bool
	// Stranded marks a status-backed .afk/{name}/ found in a checkout that
	// is not the session's own worktree (discovery's
	// KindStatusWithoutWorktree). Cleaning it archives and removes that
	// directory and leaves the checkout alone, so the Clean confirm must say
	// "worktree kept" for it, and Restart is not offered: there is no
	// worktree of its own for a host to start in.
	Stranded bool
}

// SessionCheck is one check of a session's review verdict, as the verdict
// file names it, with its result ("PASS" or "FAIL").
type SessionCheck struct {
	Name   string `json:"name"`
	Result string `json:"result"`
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
	// PlanChat reads the open planning session of the project at root as a
	// chat. It is a function so the web package stays free of the host, the
	// session and the agent CLI: the daemon wires the real read (which only
	// opens a transcript while the planning host is alive) and a test wires
	// a fake. Nil means this page cannot read planning sessions, and the
	// address says so in the chat's own words.
	PlanChat func(project, root string) planchat.Chat
	// PlanReply sends the owner's reply to the open planning session of the
	// project at root. A nil error means the reply was sent; a
	// *planchat.Refusal means nothing was sent, because the reply cannot be
	// given to the session as it is; any other error means the planning
	// host could not be reached. The refusal decision is made behind this
	// function (by planchat), never in the web package. Nil means this page
	// cannot send replies.
	PlanReply func(project, root string, r planchat.Reply) error
	// ProjectRows lists the all-projects rows, built the way the dashboard's
	// all-projects screen builds them. It is a seam rather than something
	// this package works out because the rows are built in the root package
	// from the daemon's last good project list (internal/projectrows does the
	// pure part), and the page must never read config.toml itself: the
	// daemon owns that list. Nil means no rows, like Projects.
	ProjectRows func() []projectrows.Row
	// Repository says where the project at root is pushed: its origin's
	// address with any user name and password taken out, or "" when there
	// is none or it cannot be read. The page shows it under Repository on
	// the project screen. It is a seam because finding it runs git, which
	// this package never does itself; the daemon's version is bounded by a
	// short timeout so a stuck git cannot hold a page request. Nil means
	// the page says "None set".
	Repository func(root string) string
	// TaskTokens reads one task's token record from the project at root, the
	// same read `afk tokens <task>` makes, already totalled. It is a seam
	// because that read lives in the root package (taskTokensForSurface),
	// which this package cannot import, and because the web page must agree
	// with the command to the token. It only reads: nothing is recorded.
	// Nil means the page cannot show tokens, and the task screen's Tokens
	// card says so.
	TaskTokens func(root, task string) TaskTokens
	// SetTaskStatus does what `afk ledger set` does for one task of the
	// project at root, through the one writer the command and the dashboard
	// use (setLedgerStatusForSurface in the root package), and reports what
	// happened in the command's own words. It is the only way this package
	// can change the ledger; the package doc's "handlers never write the
	// ledger" holds because the write happens behind it. Nil means the page
	// offers no way to set a status.
	SetTaskStatus func(root, id, status string, replacedBy []string) TaskStatusResult
	// RefreshUsage takes one fresh usage reading and saves it to usage.json,
	// what `afk usage --refresh` and the dashboard's refresh do, and says
	// how it went. It is synchronous and may take up to a minute (it runs
	// the agent's command-line tool), so the page never calls it inside a
	// request: the refresher in usage.go runs it on its own goroutine.
	//
	// It is a seam because the reading lives in the root package
	// (refreshUsageForSurface), which this package cannot import, and it is
	// the root's entry point as it is. That entry point does not guard
	// against two readings at once (the T7.13 amendment leaves "guarding one
	// reading at a time" to each caller), so the guard is the refresher's.
	// Nil means the page offers no way to refresh usage.
	RefreshUsage func() UsageRefreshResult
	// CleanSession, RestartSession, SetSessionLabel and NewSession are the
	// session actions of the Sessions screens (sessions_actions.go): what
	// `afk clean`, the dashboard's restart, its label editor and `afk new
	// --autonomous` do, through the root package's print-free entry points,
	// which this package cannot import. Each says what happened in a
	// SessionActionResult. Nil means the page offers no such action: no
	// button is drawn and its POST is not found.
	//
	// Every one takes the session's name and never a folder: the worktree is
	// found again by discovery behind the seam, so a request cannot point an
	// action at a folder of its choosing. phaseSeen is the phase the page
	// showed when the owner pressed Clean; the clean is refused when a fresh
	// read says otherwise, as the dashboard refuses it.
	CleanSession    func(root, name, phaseSeen string) SessionActionResult
	RestartSession  func(root, name string) SessionActionResult
	SetSessionLabel func(root, name, label string) SessionActionResult
	NewSession      func(root, name, description string) SessionActionResult
	// PlanStatus reads the planning status of the project at root from
	// .afk/plan/ alone, with the plan line worded by the dashboard's own
	// function (see plan_approve.go). It must not block: files only, no git
	// and no dialing, because the plan page and the project page call it on
	// every request. Nil means the page shows no plan line and offers no
	// approve.
	PlanStatus func(root string) PlanStatus
	// ApprovePlan does what `afk plan approve` does for the project at root,
	// through the root package's print-free entry point the dashboard's
	// approve uses, and says what happened in plain words. It re-reads the
	// planning session, the plan and the checker itself and refuses a plan
	// that cannot be approved, so the handler never checks first. It may take
	// minutes (it pushes; see planApproveWriteLimit). Nil means no approve
	// button and the approve POST is not found.
	ApprovePlan func(root string) PlanApproveResult
	// Now is the clock. Nil means time.Now.
	Now func() time.Time
	// Ticker starts the clock the live-change stream's reader runs on (see
	// live.go) and returns its channel and the function that stops it. It is
	// here so a test can decide exactly when the reader looks at the files,
	// instead of waiting on a real clock. Nil means a real time.Ticker every
	// two seconds. The interval is deliberately not a setting: the dashboard
	// re-reads the same files every two seconds, and §3 of docs/PLAN.md
	// forbids reading them faster than that.
	Ticker func() (<-chan time.Time, func())
}

// TaskTokens is one task's token record as Deps.TaskTokens reports it: a plain
// copy of what `afk tokens <task>` reads, with its own types so this package
// does not depend on the root package's.
type TaskTokens struct {
	// State is "recorded", "none" (no line for this task, or no token ledger
	// at all) or "unreadable".
	State string `json:"state"`
	// Total is the weighted cost of every line, the command's total.
	Total float64 `json:"total"`
	// Short is the total abbreviated with its unit, e.g. "2.9M weighted".
	Short string `json:"short"`
	// Phases are in the command's phase order; Roles heaviest first.
	Phases []TokenPhase `json:"phases"`
	Roles  []TokenRole  `json:"roles"`
	// Err is the reader's own words when State is "unreadable".
	Err string `json:"error"`
}

// TokenPhase is one phase's part of a task's tokens. Share is a percentage
// (0 to 100) at one decimal, as the command's share column prints it.
type TokenPhase struct {
	Phase    string  `json:"phase"`
	Weighted float64 `json:"weighted"`
	Share    float64 `json:"share"`
}

// TokenRole is one role's counts over every phase and model.
type TokenRole struct {
	Role     string  `json:"role"`
	Calls    int64   `json:"calls"`
	Input    int64   `json:"input"`
	Output   int64   `json:"output"`
	Weighted float64 `json:"weighted"`
}

// The words TaskTokens.State takes.
const (
	TokensRecorded   = "recorded"
	TokensNone       = "none"
	TokensUnreadable = "unreadable"
)

// TaskStatusResult is what Deps.SetTaskStatus reports. Outcome is "changed",
// "already" (the task already had that status; nothing was written),
// "refused" (the request could never work: an unknown status or id, or
// superseded without replacements) or "failed" (the ledger could not be
// written). Old and New are the change's real statuses and Text is the
// command's own sentence.
type TaskStatusResult struct {
	Outcome string `json:"outcome"`
	Old     string `json:"old"`
	New     string `json:"new"`
	Text    string `json:"text"`
}

// UsageRefreshResult is what Deps.RefreshUsage reports: one of the
// UsageRefresh outcomes and a plain sentence for the owner that names no
// agent. It is this package's own type, a copy of the root's result, because
// this package cannot import the root package.
type UsageRefreshResult struct {
	Outcome string `json:"outcome"`
	Text    string `json:"text"`
}

// SessionActionResult is what each session action seam reports: one of the
// SessionAction outcomes and a plain sentence for the owner. It is this
// package's own type because the root's result types cannot be imported.
type SessionActionResult struct {
	Outcome string `json:"outcome"`
	Text    string `json:"text"`
}

// The outcomes a SessionActionResult takes.
const (
	// SessionActionDone: the action happened.
	SessionActionDone = "done"
	// SessionActionRefused: nothing was done, because the request could not
	// work as it stands (the session changed, a name is taken, ...).
	SessionActionRefused = "refused"
	// SessionActionFailed: the action was tried and went wrong.
	SessionActionFailed = "failed"
)

// The outcomes a UsageRefreshResult takes. A save that failed after a good
// reading is UsageRefreshFailed: the owner's next step is the same, and the
// old reading still stands.
const (
	// UsageRefreshed: a reading was taken and saved to usage.json.
	UsageRefreshed = "refreshed"
	// UsageRefreshUnsupported: the agent cannot report its usage at all.
	UsageRefreshUnsupported = "unsupported"
	// UsageRefreshFailed: no agent, the reading failed, or it could not be
	// saved. Nothing was written, so the old reading stays.
	UsageRefreshFailed = "failed"
)

// maxBody is the most a POST may carry. An answer is a sentence or a pasted
// value; 64 KB is far more than that needs and small enough that a flood of
// requests cannot fill memory.
const maxBody = 64 << 10

// server holds what New was given plus values worked out once from the
// token. Nothing in it changes after New except hub and refresher, which
// guard themselves, so requests share it safely, and none of it is read from
// a project's files: that is the "no cache" rule.
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
	// hub is the live-change stream's shared reader and its open streams.
	// It is the one thing in the server that changes after New, and it holds
	// fingerprints, never anything a page shows, so the "no cache" rule
	// stands.
	hub *hub
	// refresher runs the background usage reading behind Refresh usage (see
	// usage.go). It is the second thing that changes after New, because
	// "a reading is running" and "the last one failed" exist nowhere else: a
	// running reading has written nothing yet, and a failed one writes no
	// file at all. It holds only that, never the reading, which the page
	// still reads fresh from usage.json, so the "no cache" rule stands.
	refresher *usageRefresher
	// assets are the embedded static files by name, hashed once in New.
	assets map[string]asset
	// refusedPage is the page that refuses a request without the token,
	// built once in New from the stylesheets' addresses (refusedPageFor).
	refusedPage []byte
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
	// Refresh usage: starts one reading in the background through
	// Deps.RefreshUsage. A POST of its own, so GET /usage stays a read that
	// can never start one, and POST /usage stays not found.
	{http.MethodPost, "/usage/refresh", (*server).refreshUsage},
	{http.MethodGet, "/p/{project}", (*server).project},
	// The plan view. A plan on the review list, whose project is not a
	// configured one yet, is served here too (findPlanProject).
	{http.MethodGet, "/p/{project}/plan", (*server).plan},
	// Approving the plan: what `afk plan approve` does, behind the confirm,
	// through Deps.ApprovePlan and nothing else (see plan_approve.go).
	{http.MethodPost, "/p/{project}/plan/approve", (*server).planApprove},
	{http.MethodGet, "/p/{project}/inbox", (*server).inboxList},
	{http.MethodGet, "/p/{project}/inbox/{id}", (*server).item},
	{http.MethodPost, "/p/{project}/inbox/{id}/answer", (*server).answer},
	// The tasks list and one task's screen. {id} is a master-plan task id.
	{http.MethodGet, "/p/{project}/tasks", (*server).tasksList},
	{http.MethodGet, "/p/{project}/tasks/{id}", (*server).task},
	// Setting a task's status: what `afk ledger set` does, behind the
	// confirm, through Deps.SetTaskStatus and nothing else.
	{http.MethodPost, "/p/{project}/tasks/{id}/status", (*server).setTaskStatus},
	// The sessions list and one session's overview. The page cannot open a
	// session's terminal (see sessions.go).
	{http.MethodGet, "/p/{project}/sessions", (*server).sessionsList},
	{http.MethodGet, "/p/{project}/sessions/{name}", (*server).session},
	// The session actions, each behind the confirm and through its Deps seam
	// and nothing else (see sessions_actions.go). POST on the list starts a
	// new autonomous session.
	{http.MethodPost, "/p/{project}/sessions", (*server).newSession},
	{http.MethodPost, "/p/{project}/sessions/{name}/clean", (*server).cleanSession},
	{http.MethodPost, "/p/{project}/sessions/{name}/restart", (*server).restartSession},
	{http.MethodPost, "/p/{project}/sessions/{name}/label", (*server).labelSession},
	{http.MethodPost, "/p/{project}/pause", (*server).pause},
	{http.MethodPost, "/p/{project}/resume", (*server).resume},
}

// New builds the handler for the page. It returns an error when the token is
// empty or a dependency is missing, so the caller finds out at start-up and
// not on the first request.
func New(cfg Config, d Deps) (http.Handler, error) {
	_, h, err := build(cfg, d)
	return h, err
}

// build is New that also hands back the server, so a test can wait for a
// background usage reading to finish (usageRefresher.wait) before it ends.
func build(cfg Config, d Deps) (*server, http.Handler, error) {
	if cfg.Token == "" {
		return nil, nil, errors.New("the web page needs a token; set [web] token in config.toml")
	}
	switch {
	case d.LoadBuild == nil:
		return nil, nil, errors.New("the web page was given no way to read the plan and ledger (LoadBuild)")
	case d.Sessions == nil:
		return nil, nil, errors.New("the web page was given no way to list sessions (Sessions)")
	case d.Usage == nil:
		return nil, nil, errors.New("the web page was given no way to read usage (Usage)")
	case d.Pause == nil, d.Resume == nil:
		return nil, nil, errors.New("the web page was given no way to pause or resume a project (Pause, Resume)")
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

	assets, err := loadAssets()
	if err != nil {
		return nil, nil, err
	}
	s.assets = assets
	s.refusedPage = []byte(refusedPageFor(s.assetURL("afk.css"), s.assetURL("page.css")))
	pages, err := parsePages(s.assetURL)
	if err != nil {
		return nil, nil, err
	}
	s.pages = pages
	s.refresher = newUsageRefresher(d.Now)
	s.hub = newHub(d, s.refresher.generation)

	mux := http.NewServeMux()
	s.register(mux, "/api")
	s.register(mux, "")
	// The live-change stream is not in routes: it has no JSON form, so it
	// must not get an /api twin. guard still wraps it like everything else.
	mux.HandleFunc("GET /events", s.events)
	// The planning chat is JSON only for now: the page that shows it comes
	// with T8.12, which will register its twin. Until then /p/{project}/plan/chat
	// must stay not found, so it is not in routes (that table gives every
	// entry both forms).
	mux.HandleFunc("GET /api/p/{project}/plan/chat", s.planChat)
	// The reply goes in the same place and for the same reason: only under
	// /api, so /p/{project}/plan/chat stays not found until T8.12.
	mux.HandleFunc("POST /api/p/{project}/plan/chat", s.planReply)
	// The confirm page behind Pause and Resume is HTML only, so it stays out
	// of routes for the same reason.
	mux.HandleFunc("GET /p/{project}/confirm/{action}", s.confirm)
	// The no-JS page behind a task's Set status… is HTML only too.
	mux.HandleFunc("GET /p/{project}/tasks/{id}/status", s.taskStatusPage)
	// The no-JS pages behind the session actions are HTML only too. The new
	// session's page is more specific than /sessions/{name}, so it wins for
	// the address /sessions/new (see sessionNewPage).
	mux.HandleFunc("GET /p/{project}/sessions/{name}/confirm/{action}", s.sessionConfirmPage)
	mux.HandleFunc("GET /p/{project}/sessions/new", s.sessionNewPage)
	// The no-JS page behind Approve the plan… is HTML only too.
	mux.HandleFunc("GET /p/{project}/plan/approve", s.planApprovePage)
	// Static files likewise have no JSON form and no /api twin.
	mux.HandleFunc("GET /static/{hash}/{file}", s.static)
	// Everything else, including a known address asked for with the wrong
	// method, gets the same plain sentence as any other error instead of the
	// standard library's bare "404 page not found".
	mux.HandleFunc("/", s.notFound)
	return s, s.guard(mux), nil
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
// "tasks", "task", "error").
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
	s.renderPage(w, r, status, name, view)
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
