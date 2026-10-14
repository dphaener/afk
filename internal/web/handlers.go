package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/planlint"
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
}

// InboxView is a project's inbox in the store's order: open and blocking
// first, then open, then answered.
type InboxView struct {
	// Project is for the page's links; the JSON does not repeat it.
	Project string       `json:"-"`
	Items   []inbox.Item `json:"items"`
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
}

// ItemView is one inbox item. The JSON is the item exactly as stored.
type ItemView struct {
	// Project is for the page's links and the answer form's address.
	Project string `json:"-"`
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
	box, _ := s.readInbox(p)
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
		Sessions:   s.d.Sessions(p.Root),
		Items:      box.Items,
		Now:        s.d.Now(),
	})
	return ProjectView{ProjectSummary: sum, Inbox: box}
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

func (s *server) usage(w http.ResponseWriter, r *http.Request) {
	s.respond(w, r, http.StatusOK, "usage", s.usageView())
}

func (s *server) project(w http.ResponseWriter, r *http.Request) {
	p, ok := s.findProject(w, r)
	if !ok {
		return
	}
	s.respond(w, r, http.StatusOK, "project", s.projectView(p))
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
	s.respond(w, r, http.StatusOK, "item", ItemView{Project: p.Name, Item: it})
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
		s.respond(w, r, http.StatusOK, "item", ItemView{Project: p.Name, Item: it})
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
	if err := act(p.Root); err != nil {
		s.fail(w, r, http.StatusInternalServerError, fmt.Sprintf("The project could not be %s: %v. Check that the project folder can be written, then try again.", done, err))
		return
	}
	s.respond(w, r, http.StatusOK, "project", s.projectView(p))
}
