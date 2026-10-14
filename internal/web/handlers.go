package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/dphaener/afk/internal/daemon"
	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/planlint"
	"github.com/dphaener/afk/internal/plansession"
	"github.com/dphaener/afk/internal/usagestate"
)

// OverviewView is the front page: every project, and the usage reading.
type OverviewView struct {
	Projects []ProjectSummary `json:"projects"`
	// Reviews are the plans waiting for approval that are not configured
	// projects. Each has a plan view and nothing else.
	Reviews []ReviewView `json:"reviews"`
	Usage   UsageView    `json:"usage"`
	// Rows are the all-projects rows from Deps.ProjectRows, in
	// projectrows.Sort's order (needs-you first). It was added beside the
	// three keys above rather than in place of Projects, so a reader of the
	// API built against those keys keeps working; never null.
	Rows []RowView `json:"rows"`
	// Page is the home page's layout of Rows and Reviews. Only the page
	// reads it, so the JSON leaves it out.
	Page overviewPage `json:"-"`
}

// ReviewView is one plan waiting for approval. Name is what its plan view's
// address uses.
type ReviewView struct {
	Name string `json:"name"`
}

// PlanQuestion is one open row of the plan's open-questions table.
type PlanQuestion struct {
	Text string `json:"text"`
	// NeededBy is the task ids the row names, never null.
	NeededBy []string `json:"needed_by"`
	Line     int      `json:"line"`
}

// PlanView is a project's master plan, read-only: the checker's counts and
// findings, the open questions, and the text. The JSON carries the plan as
// written; only the page renders it.
type PlanView struct {
	Project string `json:"project"`
	// Path is where the plan is, as ReadPlan reported it.
	Path string `json:"path"`
	// WaitingForApproval is true for a plan on the review list whose project
	// is not a configured one yet.
	WaitingForApproval bool             `json:"waiting_for_approval"`
	Summary            planlint.Summary `json:"summary"`
	// Findings is everything the checker found, errors and warnings.
	Findings  []planlint.Finding `json:"findings"`
	Questions []PlanQuestion     `json:"questions"`
	Plan      string             `json:"plan"`
	// Problem is why the plan could not be read, and empty otherwise. The
	// counts and lists are then empty and mean nothing.
	Problem string `json:"problem"`
}

// Errors is the checker's errors without its warnings. The page shows only
// these: an error stops afk reading the plan correctly, a warning does not,
// and a plan of any size has a screenful of warnings.
func (v PlanView) Errors() []planlint.Finding {
	var out []planlint.Finding
	for _, f := range v.Findings {
		if f.Level == planlint.LevelError {
			out = append(out, f)
		}
	}
	return out
}

// Rendered is the plan as HTML, for the page. It is a method and not a field
// so the JSON API, which gives the text as written, never pays for it.
func (v PlanView) Rendered() template.HTML {
	return renderPlan(v.Plan)
}

// UsageView is usage.json as stored, plus whether there was a file at all.
// Nothing is worked out here: the governor's decision is shown as it was
// recorded, because the thresholds may have changed since. `provider` is the
// stored string and nothing more.
type UsageView struct {
	// Known is false when no reading has been stored yet. The other fields
	// are then empty, and `at` is left out.
	Known          bool                 `json:"known"`
	At             *time.Time           `json:"at,omitempty"`
	Provider       string               `json:"provider"`
	Windows        []usagestate.Window  `json:"windows"`
	OverageEnabled bool                 `json:"overage_enabled"`
	Source         string               `json:"source"`
	Governor       *usagestate.Governor `json:"governor"`
	// Refresh is the Refresh usage button's state (see usage.go). Only the
	// usage screen fills it in, and only when this daemon can refresh
	// (Deps.RefreshUsage is set); otherwise it is left out, which is also
	// why the overview's copy of this view never carries it.
	Refresh *UsageRefreshView `json:"refresh,omitempty"`
	// Page is the usage screen's layout of the fields above (the decision's
	// badge, one meter per window). Only the page reads it, and only the
	// usage route fills it in, so the JSON leaves it out.
	Page usagePage `json:"-"`
}

// InboxView is a project's inbox in the store's order: open and blocking
// first, then open, then answered.
type InboxView struct {
	// Project is for the page's links; the JSON does not repeat it.
	Project string       `json:"-"`
	Items   []inbox.Item `json:"items"`
	// Page is the inbox screen's layout of Items. Only the page reads it,
	// and only the inbox route fills it in, so the JSON leaves it out.
	Page inboxPage `json:"-"`
	// Unreadable counts the files that look like items but could not be
	// read, so the page can say that some are missing from the list.
	Unreadable int `json:"unreadable"`
	// Problem is why the inbox folder could not be read at all, and empty
	// otherwise.
	Problem string `json:"problem"`
}

// ProjectView is one project's page: its summary, with the inbox beside it.
// The summary's keys sit at the top level of the JSON, so the answer to a
// pause or a resume (which is this view again) reads as the project's summary.
type ProjectView struct {
	ProjectSummary
	Inbox InboxView `json:"inbox"`
	// Page is the project screen laid out (see projectPageFor). Only the
	// project page's GET fills it in and only the page reads it, so the JSON
	// leaves it out and the API's answer is the one it always was.
	Page projectPage `json:"-"`
}

// ItemView is one inbox item. The JSON is the item exactly as stored.
type ItemView struct {
	// Project is for the page's links and the answer form's address.
	Project string `json:"-"`
	// DoneCode is the toast code the page redirect carries after an answer
	// (see toastTexts). It is set only for a page answer and left out of the
	// JSON.
	DoneCode string `json:"-"`
	// Page is the item laid out for the page (see itemPageFor); it is left
	// out of the JSON.
	Page itemPage `json:"-"`
	inbox.Item
}

// findProject returns the project named in the address. An unknown name is
// answered here, so a handler only has to return when ok is false.
//
// It looks only at configured projects. A plan waiting for approval has no
// build, so the project page, the inbox and above all the pause and resume
// buttons must not answer for it: findPlanProject is the one way in for
// those, and only the plan view uses it.
func (s *server) findProject(w http.ResponseWriter, r *http.Request) (Project, bool) {
	if p, ok := named(s.projects(), r.PathValue("project")); ok {
		return p, true
	}
	s.fail(w, r, http.StatusNotFound, "There is no project with that name in afk's config.toml. Check the address, or go back to the overview.")
	return Project{}, false
}

// findPlanProject returns the project whose plan the address asks for:
// a configured project first, then a plan waiting for approval. waiting is
// true for the second kind. An unknown name is answered here.
func (s *server) findPlanProject(w http.ResponseWriter, r *http.Request) (p Project, waiting, ok bool) {
	name := r.PathValue("project")
	configured := s.projects()
	if p, ok := named(configured, name); ok {
		return p, false, true
	}
	if p, ok := named(s.reviews(configured), name); ok {
		return p, true, true
	}
	s.fail(w, r, http.StatusNotFound, "There is no project with that name in afk's config.toml, and no plan with that name is waiting for approval. Check the address, or go back to the overview.")
	return Project{}, false, false
}

func named(ps []Project, name string) (Project, bool) {
	for _, p := range ps {
		if p.Name == name {
			return p, true
		}
	}
	return Project{}, false
}

// projects asks for the configured projects, now. See Deps.Projects for why
// it is asked every time.
func (s *server) projects() []Project {
	if s.d.Projects == nil {
		return nil
	}
	return s.d.Projects()
}

// reviews asks for the plans waiting for approval, now, and leaves out any
// that a configured project already covers: one with the same folder (it was
// approved a moment ago and the list has not caught up) or the same name
// (the address /p/{name}/plan means the configured project, so a link to the
// other plan under that name would open the wrong one).
func (s *server) reviews(configured []Project) []Project {
	if s.d.Reviews == nil {
		return nil
	}
	var out []Project
	for _, rv := range s.d.Reviews() {
		taken := false
		for _, p := range configured {
			if p.Name == rv.Name || p.Root == rv.Root {
				taken = true
				break
			}
		}
		if !taken {
			out = append(out, rv)
		}
	}
	return out
}

// planView reads the plan and runs the checker over it, now. Nothing is kept
// between requests: the planning session rewrites the plan while the owner
// reads it, and the counts must be the counts of the text shown.
func (s *server) planView(p Project, waiting bool) PlanView {
	v := PlanView{Project: p.Name, WaitingForApproval: waiting, Findings: []planlint.Finding{}, Questions: []PlanQuestion{}}
	if s.d.ReadPlan == nil {
		v.Problem = "This afk daemon was started without a way to read plans. This is a fault in afk; please report it."
		return v
	}
	path, data, err := s.d.ReadPlan(p.Root)
	v.Path = path
	if err != nil {
		v.Problem = fmt.Sprintf("The plan could not be read: %v. Check that the file exists and that the project's afk.toml names it.", err)
		return v
	}
	rep := planlint.Lint(data)
	v.Summary = rep.Summary
	if rep.Findings != nil {
		v.Findings = rep.Findings
	}
	// The same reader the checker and the daemon use, so "open" means here
	// what it means to them.
	for _, q := range masterplan.Questions(data) {
		if !q.Open {
			continue
		}
		needed := q.NeededBy
		if needed == nil {
			needed = []string{}
		}
		v.Questions = append(v.Questions, PlanQuestion{Text: q.Text, NeededBy: needed, Line: q.Line})
	}
	v.Plan = string(data)
	return v
}

func (s *server) plan(w http.ResponseWriter, r *http.Request) {
	p, waiting, ok := s.findPlanProject(w, r)
	if !ok {
		return
	}
	s.respond(w, r, http.StatusOK, "plan", s.planView(p, waiting))
}

func (s *server) store(p Project) inbox.Store {
	return inbox.Store{Dir: inbox.Dir(p.Root), Now: s.d.Now}
}

// readInbox lists a project's inbox. The error is returned as well as being
// put in the view: the inbox route fails on it, while the project page shows
// the rest of the project with the problem in place of the list.
func (s *server) readInbox(p Project) (InboxView, error) {
	v := InboxView{Project: p.Name, Items: []inbox.Item{}}
	items, unreadable, err := s.store(p).List()
	if err != nil {
		v.Problem = fmt.Sprintf("The inbox could not be read: %v. Check that the folder exists and can be read.", err)
		return v, err
	}
	if items != nil {
		v.Items = items
	}
	v.Unreadable = len(unreadable)
	return v, nil
}

// projectView reads everything about one project, from the files, now.
func (s *server) projectView(p Project) ProjectView {
	v, _ := s.projectViewSessions(p)
	return v
}

// projectViewSessions is projectView that also hands back the sessions it
// read, so the project page can list the running ones without asking
// discovery a second time.
func (s *server) projectViewSessions(p Project) (ProjectView, []SessionView) {
	box, _ := s.readInbox(p)
	sessions := s.d.Sessions(p.Root)
	tasks, l, loadErr := s.d.LoadBuild(p.Root)
	state, stateKnown := daemonstate.ReadState(p.Root)
	stopTask, stopReason, stopped := daemonstate.ReadStop(p.Root)
	sum := Summarize(SummaryInput{
		Name:       p.Name,
		Tasks:      tasks,
		Ledger:     l,
		LoadErr:    loadErr,
		State:      state,
		StateKnown: stateKnown,
		Paused:     daemonstate.Paused(p.Root),
		Stopped:    stopped,
		StopTask:   stopTask,
		StopReason: stopReason,
		Sessions:   sessions,
		Items:      box.Items,
		Now:        s.d.Now(),
	})
	return ProjectView{ProjectSummary: sum, Inbox: box}, sessions
}

func (s *server) usageView() UsageView {
	v := UsageView{Windows: []usagestate.Window{}}
	f, ok := s.d.Usage()
	if !ok {
		return v
	}
	v.Known = true
	at := f.At
	v.At = &at
	v.Provider = f.Provider
	if f.Windows != nil {
		v.Windows = f.Windows
	}
	v.OverageEnabled = f.OverageEnabled
	v.Source = f.Source
	v.Governor = f.Governor
	return v
}

func (s *server) overview(w http.ResponseWriter, r *http.Request) {
	configured := s.projects()
	v := OverviewView{Projects: make([]ProjectSummary, 0, len(configured)), Reviews: []ReviewView{}, Usage: s.usageView()}
	for _, p := range configured {
		v.Projects = append(v.Projects, s.projectView(p).ProjectSummary)
	}
	for _, rv := range s.reviews(configured) {
		v.Reviews = append(v.Reviews, ReviewView{Name: rv.Name})
	}
	// The rows come from the seam, already sorted, and never from a list
	// this package read: the daemon owns the project list (see
	// Deps.ProjectRows).
	v.Rows = []RowView{}
	if s.d.ProjectRows != nil {
		for _, row := range s.d.ProjectRows() {
			v.Rows = append(v.Rows, rowView(row))
		}
	}
	v.Page = overviewPageFor(v.Rows, v.Reviews)
	s.respond(w, r, http.StatusOK, "overview", v)
}

// usage is GET /usage. It only reads: it never starts a reading, however
// old the stored one is (that is POST /usage/refresh's job, and the owner's
// choice).
func (s *server) usage(w http.ResponseWriter, r *http.Request) {
	v := s.usageView()
	if s.d.RefreshUsage != nil {
		v.Refresh = refreshViewFor(s.refresher.snapshot(), s.d.Now())
	}
	v.Page = usagePageFor(v)
	s.respond(w, r, http.StatusOK, "usage", v)
}

func (s *server) project(w http.ResponseWriter, r *http.Request) {
	p, ok := s.findProject(w, r)
	if !ok {
		return
	}
	v, sessions := s.projectViewSessions(p)
	// The screen's extra reads (the row, the planning state, the origin) are
	// made only for the page: the JSON leaves Page out, so an API reader
	// would pay for a git call it never sees.
	if !isAPI(r.URL.Path) {
		v.Page = s.projectPage(p, v, sessions)
	}
	s.respond(w, r, http.StatusOK, "project", v)
}

// projectPage reads what the project screen needs besides the summary and
// lays it out. Every read here is a file read or a Deps function; the one
// that runs a program (Repository, git config) is bounded by its own
// timeout in the daemon's wiring.
func (s *server) projectPage(p Project, v ProjectView, sessions []SessionView) projectPage {
	in := projectPageInput{
		Summary:  v.ProjectSummary,
		Sessions: sessions,
		Items:    v.Inbox.Items,
		Folder:   p.Root,
		Now:      s.d.Now(),
	}
	if s.d.ProjectRows != nil {
		for _, row := range s.d.ProjectRows() {
			if row.Name == p.Name {
				in.Row = &row
				break
			}
		}
	}
	// The row knows whether the plan waits for approval (plan-reviews.json,
	// read by the daemon's seam); this package has no other way to tell for
	// a configured project, because Deps.Reviews leaves those out.
	if in.Row != nil {
		in.PlanWaiting = in.Row.PlanWaiting
	}
	in.Planning, in.PlanningOK, in.PlanningErr = plansession.ReadState(p.Root)
	if s.d.Repository != nil {
		in.Repository = s.d.Repository(p.Root)
	}
	return projectPageFor(in)
}

func (s *server) inboxList(w http.ResponseWriter, r *http.Request) {
	p, ok := s.findProject(w, r)
	if !ok {
		return
	}
	v, err := s.readInbox(p)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, v.Problem)
		return
	}
	v.Page = inboxPageFor(v.Project, v.Items, s.d.Now())
	s.respond(w, r, http.StatusOK, "inbox", v)
}

func (s *server) item(w http.ResponseWriter, r *http.Request) {
	p, ok := s.findProject(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	// The id is checked before it is used for anything: ValidID is what
	// keeps a value from the address from being read as a path.
	if !inbox.ValidID(id) {
		s.noSuchItem(w, r)
		return
	}
	it, err := s.store(p).Get(id)
	if errors.Is(err, inbox.ErrNotFound) {
		s.noSuchItem(w, r)
		return
	}
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, fmt.Sprintf("The inbox item's file is damaged and could not be read: %v. Fix or delete the file in the project's .afk/inbox folder.", err))
		return
	}
	s.respond(w, r, http.StatusOK, "item", ItemView{Project: p.Name, Item: it, Page: itemPageFor(it, s.d.Now())})
}

func (s *server) noSuchItem(w http.ResponseWriter, r *http.Request) {
	s.fail(w, r, http.StatusNotFound, "There is no inbox item with that id in this project. Check the address, or go back to the project page and pick it from the list.")
}

// answer stores the owner's answer and does nothing else. The task that was
// waiting is un-parked by the build loop on its next pass, when it finds the
// item answered; this handler does not touch the ledger (see the package
// doc).
func (s *server) answer(w http.ResponseWriter, r *http.Request) {
	p, ok := s.findProject(w, r)
	if !ok {
		return
	}
	text, err := answerText(r)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			s.tooLarge(w, r)
			return
		}
		s.fail(w, r, http.StatusBadRequest, "The request could not be read. Send the answer in the form field \"answer\", or as JSON like {\"answer\": \"your text\"}.")
		return
	}

	it, err := s.store(p).Answer(r.PathValue("id"), text, inbox.ViaWeb)
	switch {
	case err == nil:
		code := ""
		if !isAPI(r.URL.Path) {
			code = s.answerCode(p, it)
		}
		s.respond(w, r, http.StatusOK, "item", ItemView{Project: p.Name, Item: it, DoneCode: code})
	case errors.Is(err, inbox.ErrNotFound):
		s.noSuchItem(w, r)
	case errors.Is(err, inbox.ErrAlreadyAnswered):
		s.respond(w, r, http.StatusConflict, "error", ErrorView{
			Status: http.StatusConflict,
			Error:  "This item already has an answer, and the first answer is the one that is kept. Nothing was changed.",
			Item:   &it,
		})
	case errors.Is(err, inbox.ErrEmptyAnswer):
		s.fail(w, r, http.StatusBadRequest, "The answer was empty. Type an answer and send it again.")
	default:
		s.fail(w, r, http.StatusInternalServerError, fmt.Sprintf("The answer could not be saved: %v. Nothing was changed; try again.", err))
	}
}

// answerCode picks the toast the inbox list shows after a page answer: "done"
// for an owner task, "ready" when the answer leaves its task with nothing
// else to wait for, "answered" otherwise. It only reads; the build loop is
// what un-parks the task, so "ready" says what is about to happen and the
// ledger is not touched here.
func (s *server) answerCode(p Project, it inbox.Item) string {
	if isOwnerTask(it) {
		return "done"
	}
	if it.Task == "" {
		return "answered"
	}
	_, l, err := s.d.LoadBuild(p.Root)
	if err != nil {
		return "answered"
	}
	items, _, err := s.store(p).List()
	if err != nil {
		return "answered"
	}
	var entry *ledger.Entry
	if e, ok := l.Get(it.Task); ok {
		entry = &e
	}
	if readyAgain(it, entry, items) {
		return "ready"
	}
	return "answered"
}

// isOwnerTask reports whether an item is the one afk raises for a task only
// the owner can do (see daemon.OwnerTitlePrefix): answering it means the
// task is done, not that a question was answered.
func isOwnerTask(it inbox.Item) bool {
	return it.Kind == "external_action" && it.Blocking && it.Task != "" && strings.HasPrefix(it.Title, daemon.OwnerTitlePrefix)
}

// readyAgain is the pure decision behind the "ready again" toast. It is true
// only when the answered item has a task, that task is parked in the ledger,
// and no other open item is one it is parked on or an open blocking item for
// the same task. items is the inbox as it reads after the answer; the
// answered item itself is skipped either way.
func readyAgain(answered inbox.Item, entry *ledger.Entry, items []inbox.Item) bool {
	if answered.Task == "" || entry == nil || entry.Status != ledger.Parked {
		return false
	}
	for _, other := range items {
		if other.ID == answered.ID || !other.Open() {
			continue
		}
		if slices.Contains(entry.ParkedOn, other.ID) {
			return false
		}
		if other.Blocking && other.Task == answered.Task {
			return false
		}
	}
	return true
}

// answerText reads the answer from the body: JSON when the request says its
// body is JSON, the form field otherwise. Only the body is read. An `answer`
// in the address is ignored, so that an address someone was sent cannot
// carry an answer with it.
func answerText(r *http.Request) (string, error) {
	// A missing or malformed Content-Type is not an error here; it just
	// is not JSON, and the form reading below decides what it holds.
	if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct == "application/json" {
		var body struct {
			Answer string `json:"answer"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return "", err
		}
		return body.Answer, nil
	}
	if err := r.ParseForm(); err != nil {
		return "", err
	}
	// The page's form has a free-text field named "answer" and, for an item
	// with options, radio buttons named "choice". Typing something is a
	// deliberate act and the preselected radio is not, so the text wins and
	// the choice is used only when the text is blank.
	if text := strings.TrimSpace(r.PostForm.Get("answer")); text != "" {
		return text, nil
	}
	return r.PostForm.Get("choice"), nil
}

// confirm is the no-JS page behind the project page's Pause and Resume links.
// It is not in routes: it has no JSON form, so it must not get an /api twin.
// Only pause and resume exist; any other action is the same 404 as an unknown
// project, so the address maps nothing.
func (s *server) confirm(w http.ResponseWriter, r *http.Request) {
	p, ok := s.findProject(w, r)
	if !ok {
		return
	}
	var sheet ConfirmSheet
	switch r.PathValue("action") {
	case "pause":
		sheet = confirmSheetFor(p.Name, false)
	case "resume":
		sheet = confirmSheetFor(p.Name, true)
	default:
		s.fail(w, r, http.StatusNotFound, "There is nothing to confirm at this address. Go back to the project and use its buttons.")
		return
	}
	s.renderPage(w, r, http.StatusOK, "confirm", ConfirmView{Project: p.Name, Sheet: sheet})
}

func (s *server) pause(w http.ResponseWriter, r *http.Request) {
	s.setPaused(w, r, s.d.Pause, "paused")
}

func (s *server) resume(w http.ResponseWriter, r *http.Request) {
	s.setPaused(w, r, s.d.Resume, "resumed")
}

// setPaused runs the injected pause or resume and answers with the project
// as it reads afterwards, from the files, so the caller sees what is true now
// and not what the button meant to do.
func (s *server) setPaused(w http.ResponseWriter, r *http.Request, act func(root string) error, done string) {
	p, ok := s.findProject(w, r)
	if !ok {
		return
	}
	if !s.confirmedRequest(r) {
		verb, button := "pausing", "Pause project"
		if done == "resumed" {
			verb, button = "resuming", "Resume project"
		}
		s.fail(w, r, http.StatusBadRequest, fmt.Sprintf("Nothing changed: %s needs the confirm. Open the project page and press %s.", verb, button))
		return
	}
	if err := act(p.Root); err != nil {
		s.fail(w, r, http.StatusInternalServerError, fmt.Sprintf("The project could not be %s: %v. Check that the project folder can be written, then try again.", done, err))
		return
	}
	s.respond(w, r, http.StatusOK, "project", s.projectView(p))
}

// confirmedRequest reads the confirm off a pause or resume request. The
// confirm used to be only a dialog in the browser, so any POST that reached
// the server paused the project; now the server asks for it too. A body that
// cannot be read counts as no confirm, so the safe answer is nothing changed.
func (s *server) confirmedRequest(r *http.Request) bool {
	b, err := readPostBody(r)
	if err != nil {
		return false
	}
	return b.confirmed()
}

// postBody is a POST's body read once: the JSON bytes when the request says
// its body is JSON, the form otherwise. The status POST needs the confirm and
// its own fields from the same body, and a JSON body can only be read once,
// so the confirm rule reads from this rather than from the request.
type postBody struct {
	contentType string
	form        url.Values
	json        []byte
}

// readPostBody reads the body as confirmed expects it. Only the body is read:
// the address's query never counts (see confirmed).
func readPostBody(r *http.Request) (postBody, error) {
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if ct == "application/json" {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return postBody{contentType: ct}, err
		}
		return postBody{contentType: ct, json: body}, nil
	}
	if err := r.ParseForm(); err != nil {
		return postBody{contentType: ct}, err
	}
	return postBody{contentType: ct, form: r.PostForm}, nil
}

func (b postBody) confirmed() bool { return confirmed(b.contentType, b.form, b.json) }

// confirmed is the pure decision: a JSON body must hold {"confirm": true};
// anything else must be a form with confirm=yes. Only the body counts, so an
// address someone was sent cannot carry the confirm with it.
func confirmed(contentType string, form url.Values, jsonBody []byte) bool {
	if contentType == "application/json" {
		var body struct {
			Confirm bool `json:"confirm"`
		}
		if err := json.Unmarshal(jsonBody, &body); err != nil {
			return false
		}
		return body.Confirm
	}
	return form.Get("confirm") == "yes"
}
