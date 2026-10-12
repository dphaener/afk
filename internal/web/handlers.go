package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/usagestate"
)

// OverviewView is the front page: every project, and the usage reading.
type OverviewView struct {
	Projects []ProjectSummary `json:"projects"`
	Usage    UsageView        `json:"usage"`
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
func (s *server) findProject(w http.ResponseWriter, r *http.Request) (Project, bool) {
	name := r.PathValue("project")
	for _, p := range s.d.Projects {
		if p.Name == name {
			return p, true
		}
	}
	s.fail(w, r, http.StatusNotFound, "There is no project with that name in afk's config.toml. Check the address, or go back to the overview.")
	return Project{}, false
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
	v := OverviewView{Projects: make([]ProjectSummary, 0, len(s.d.Projects)), Usage: s.usageView()}
	for _, p := range s.d.Projects {
		v.Projects = append(v.Projects, s.projectView(p).ProjectSummary)
	}
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
