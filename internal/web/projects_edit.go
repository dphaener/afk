package web

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// This file is the page's add and remove of a project (T8.16): POST
// /projects/add and POST /projects/remove, each with an /api twin. They do
// what `afk project add` and `afk project remove` do, through Deps.AddProject
// and Deps.RemoveProject and nothing else, so the page, the commands and the
// dashboard refuse for the same reasons in the same words.
//
// Four rules hold here.
//
// Nothing reaches a seam without the confirm in the body. Adding a folder
// makes the daemon build it, and removing one stops it watching; neither may
// happen because a link was followed.
//
// Remove never takes a folder from the request. The name posted is looked up
// in the daemon's own list (Deps.Projects) and only a listed name reaches the
// seam, so a crafted POST can only ever remove a project the page shows.
//
// This package never reads or writes config.toml. The seams do that in the
// root package, and the overview keeps showing the daemon's last good list,
// which it re-reads at the top of its next pass. So a change that went
// through is not on the overview yet, and the page says so until it is
// (projectPending).
//
// Every sentence the owner reads about the change comes from the entry
// point's result (its Text and Name), never from the request, except the two
// this file says before any seam is called.

// projectPendingTTL is how long a pending notice is shown at most. The daemon
// re-reads the list every few seconds, so a change still not shown after ten
// minutes means the daemon is not running or its pass is stuck, and the
// notice would only go on saying "within a few seconds" untruthfully.
const projectPendingTTL = 10 * time.Minute

// projectPendingMax is the most changes remembered at once; the oldest is
// dropped to make room. Each needs a confirmed POST that went through, so 32
// is far more than one owner makes in ten minutes, and small enough that no
// flood can fill memory.
const projectPendingMax = 32

// The sentences this file says before any seam is called.
const (
	projectAddNoConfirm    = "Nothing changed: adding a project needs the confirm. Open the overview and press Add an existing project."
	projectRemoveNoConfirm = "Nothing changed: removing a project needs the confirm. Open the overview and press Remove a project."
	projectAddNoFolder     = "Nothing changed: type the project's folder, as a full path or starting with ~ for your home folder."
	projectAddUnreadable   = `Nothing changed: the request could not be read. Send it as JSON like {"folder": "~/code/shop", "name": "", "confirm": true}.`
	projectRemoveUnread    = `Nothing changed: the request could not be read. Send it as JSON like {"name": "shop", "confirm": true}.`
	projectRemoveNoName    = "Nothing changed: no project was named, so there is nothing to remove. Choose one from the list."
	// projectEditUnknown replaces an outcome this page does not know, so the
	// owner is never told a change happened when it may not have.
	projectEditUnknown = "afk gave an answer this page does not know. This is a fault in afk; please report it. Run afk project list to see the list now."
	// projectEditNoText fills a refusal or failure that came with no
	// sentence, so the error page never says nothing.
	projectEditNoText = "Nothing was changed, and no reason was given. Run afk project list to see the list now."
)

// ProjectEditResult is what Deps.AddProject and Deps.RemoveProject report:
// one of the Project outcomes, the project's name and folder as the entry
// point saw them, and its sentence for the owner. It is this package's own
// type, a copy of the root's result, because this package cannot import the
// root package; the outcome strings are the dashboard's (internal/ui), so one
// adapter wording serves both.
type ProjectEditResult struct {
	Outcome string `json:"outcome"`
	Name    string `json:"name"`
	Root    string `json:"root"`
	Text    string `json:"text"`
}

// The outcomes a ProjectEditResult takes.
const (
	// ProjectAdded: the folder is on the list now.
	ProjectAdded = "added"
	// ProjectAlreadyListed: the folder was on the list already; nothing
	// changed.
	ProjectAlreadyListed = "already"
	// ProjectRemoved: the project is off the list now.
	ProjectRemoved = "removed"
	// ProjectNotListed: there was no entry to remove; nothing changed.
	ProjectNotListed = "not_listed"
	// ProjectRefused: nothing changed, because the request could not work
	// (not a folder, not a git repository, a name that clashes, a mistake in
	// the file).
	ProjectRefused = "refused"
	// ProjectFailed: the file could not be read or written.
	ProjectFailed = "failed"
)

// canAddProject and canRemoveProject report whether the page offers each at
// all: without its seam there is nothing to do it with, so the POST is not
// found and no page may lead to it.
func (s *server) canAddProject() bool    { return s.d.AddProject != nil }
func (s *server) canRemoveProject() bool { return s.d.RemoveProject != nil }

// projectEditStatus is the HTTP status for an outcome. An outcome this page
// does not know never reaches here: projectEditNormal turns it into failed.
func projectEditStatus(outcome string) int {
	switch outcome {
	case ProjectAdded, ProjectAlreadyListed, ProjectRemoved:
		return http.StatusOK
	case ProjectRefused, ProjectNotListed:
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

// projectEditDone is the ?done= code a page POST redirects with, or "" when
// the outcome is not one that redirects. The code is fixed text chosen from
// the outcome, never anything the request carried.
func projectEditDone(outcome string) string {
	switch outcome {
	case ProjectAdded:
		return "project_added"
	case ProjectRemoved:
		return "project_removed"
	case ProjectAlreadyListed:
		return "project_unchanged"
	}
	return ""
}

// projectEditNormal makes sure a seam's answer is one the page can say: an
// unknown outcome becomes failed with a sentence saying so, and a refusal or
// failure with no sentence gets one.
func projectEditNormal(res ProjectEditResult) ProjectEditResult {
	switch res.Outcome {
	case ProjectAdded, ProjectAlreadyListed, ProjectRemoved:
	case ProjectRefused, ProjectNotListed, ProjectFailed:
		if strings.TrimSpace(res.Text) == "" {
			res.Text = projectEditNoText
		}
	default:
		res = ProjectEditResult{Outcome: ProjectFailed, Name: res.Name, Root: res.Root, Text: projectEditUnknown}
	}
	return res
}

// addProject is POST /projects/add and /api/projects/add.
func (s *server) addProject(w http.ResponseWriter, r *http.Request) {
	if !s.canAddProject() {
		s.notFound(w, r)
		return
	}
	b, ok := s.readProjectEdit(w, r, projectAddNoConfirm)
	if !ok {
		return
	}
	folder, okF := bodyField(b, "folder")
	name, okN := bodyField(b, "name")
	if !okF || !okN {
		s.answerProjectEdit(w, r, ProjectEditResult{Outcome: ProjectRefused, Text: projectAddUnreadable}, false, projectForm{})
		return
	}
	folder, name = strings.TrimSpace(folder), strings.TrimSpace(name)
	if folder == "" {
		s.answerProjectEdit(w, r, ProjectEditResult{Outcome: ProjectRefused, Text: projectAddNoFolder}, false, projectForm{add: true, folder: folder, name: name})
		return
	}
	// The typed text goes to the seam as it is: resolving ~ and refusing a
	// relative path is the root's, which knows the home folder.
	res := projectEditNormal(s.d.AddProject(folder, name))
	s.answerProjectEdit(w, r, res, s.pending.record(res, s.d.Now()), projectForm{add: true, folder: folder, name: name})
}

// removeProject is POST /projects/remove and /api/projects/remove. Only a
// name on the daemon's list reaches the seam.
func (s *server) removeProject(w http.ResponseWriter, r *http.Request) {
	if !s.canRemoveProject() {
		s.notFound(w, r)
		return
	}
	b, ok := s.readProjectEdit(w, r, projectRemoveNoConfirm)
	if !ok {
		return
	}
	name, okN := bodyField(b, "name")
	if !okN {
		s.answerProjectEdit(w, r, ProjectEditResult{Outcome: ProjectRefused, Text: projectRemoveUnread}, false, projectForm{})
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		s.answerProjectEdit(w, r, ProjectEditResult{Outcome: ProjectNotListed, Text: projectRemoveNoName}, false, projectForm{})
		return
	}
	p, listed := named(s.projects(), name)
	if !listed {
		// The name is echoed here, and only here: it is what the owner
		// asked for, and the sentence says it is not on the list. The page
		// escapes it like every other value.
		s.answerProjectEdit(w, r, ProjectEditResult{Outcome: ProjectNotListed, Name: name,
			Text: name + " is not in afk's project list, so there is nothing to remove."}, false, projectForm{})
		return
	}
	res := projectEditNormal(s.d.RemoveProject(p.Name))
	s.answerProjectEdit(w, r, res, s.pending.record(res, s.d.Now()), projectForm{})
}

// readProjectEdit reads the body once and checks the confirm. It answers the
// request itself and returns false when the body is too large or the confirm
// is missing; a body that cannot be read counts as no confirm, so the safe
// answer is nothing changed.
func (s *server) readProjectEdit(w http.ResponseWriter, r *http.Request, noConfirm string) (postBody, bool) {
	b, err := readPostBody(r)
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		s.tooLarge(w, r)
		return b, false
	}
	if err != nil || !b.confirmed() {
		if isAPI(r.URL.Path) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": noConfirm, "reason": "confirm_required"})
			return b, false
		}
		s.fail(w, r, http.StatusBadRequest, noConfirm)
		return b, false
	}
	return b, true
}

// projectEditAnswer is the JSON answer of both POSTs. Pending is true when the
// change was noted as not on the overview yet (see projectPending).
type projectEditAnswer struct {
	Result  ProjectEditResult `json:"result"`
	Pending bool              `json:"pending"`
}

// projectForm is what a refused page POST needs to draw the page it came
// from again: the add form with what was typed, or (add false) the remove
// screen, which has nothing typed to keep.
type projectForm struct {
	add          bool
	folder, name string
}

// answerProjectEdit writes the answer for a result: JSON under /api; on the
// page a redirect to the overview with a ?done= code for a change or a no
// change, or the page the POST came from again (the add form with what was
// typed, the remove screen) at the outcome's status with the entry point's
// sentence as it is in an error notice.
func (s *server) answerProjectEdit(w http.ResponseWriter, r *http.Request, res ProjectEditResult, pending bool, form projectForm) {
	status := projectEditStatus(res.Outcome)
	if isAPI(r.URL.Path) {
		writeJSON(w, status, projectEditAnswer{Result: res, Pending: pending})
		return
	}
	if code := projectEditDone(res.Outcome); code != "" {
		w.Header().Set("Location", "/?done="+code)
		w.WriteHeader(http.StatusSeeOther)
		return
	}
	if form.add {
		s.renderPage(w, r, status, "project_add", ProjectAddPageView{Folder: form.folder, Name: form.name, Notice: res.Text})
		return
	}
	s.renderPage(w, r, status, "project_remove", s.removeScreen(res.Text))
}

// ProjectAddPageView is the add form. Folder and Name are what the owner
// typed, kept when a refusal redraws the form; Notice is the entry point's
// sentence, or "". The page has no JSON form.
type ProjectAddPageView struct {
	Folder, Name, Notice string
}

// projectChoice is one project of the remove screen: its name and the folder
// afk would stop watching, both from the daemon's list.
type projectChoice struct {
	Name, Folder string
}

// ProjectRemovePageView is the remove screen: the daemon's projects to choose
// from, the error notice of a refused remove, and the sheet the script opens.
type ProjectRemovePageView struct {
	Projects []projectChoice
	Notice   string
	Sheet    sessionSheet
}

// ProjectRemoveConfirmView is the no-JavaScript confirm page: the same
// question the sheet asks, drawn from the same sheet.
type ProjectRemoveConfirmView struct {
	Name  string
	Sheet sessionSheet
}

// projectRemoveBody is the sentence under the question. Folder is named, and
// what is left alone, so Remove cannot be read as deleting anything.
const projectRemoveBody = "afk stops watching it. These are not touched: the folder %s, its plan, its sessions. A task that is running is left to finish."

// projectRemoveSheet is the remove question. With a name it is the page's
// words for that project. Without one (the dialog on the remove screen) it
// holds {name} and {folder}, which confirm.js fills from the chosen radio, so
// the script writes none of the words.
func projectRemoveSheet(name, folder string) sessionSheet {
	sh := sessionSheet{
		ID: "confirm-remove-project", Action: "remove",
		Button: "Remove", Loading: "Removing", Danger: true, Safe: "Keep it",
		Path: "/projects/remove", Page: "/projects/remove/confirm",
	}
	if name == "" {
		fill := "Remove {name} from afk?"
		body := fmt.Sprintf(projectRemoveBody, "{folder}")
		sh.Title, sh.TitleFill = "Remove this project from afk?", fill
		sh.Body = []sheetLine{{Text: body, Fill: body}}
		sh.Fields = []sheetField{{Name: "name", Fill: true}}
		return sh
	}
	sh.Title = "Remove " + name + " from afk?"
	sh.Body = []sheetLine{{Text: fmt.Sprintf(projectRemoveBody, folder)}}
	sh.Fields = []sheetField{{Name: "name", Value: name}}
	return sh
}

// removeScreen is the remove screen's view over the daemon's list now.
func (s *server) removeScreen(notice string) ProjectRemovePageView {
	v := ProjectRemovePageView{Notice: notice, Sheet: projectRemoveSheet("", "")}
	for _, p := range s.projects() {
		v.Projects = append(v.Projects, projectChoice{Name: p.Name, Folder: p.Root})
	}
	return v
}

// addProjectPage is GET /projects/add. Page only: it has no JSON form, so it
// is not in routes and gets no /api twin.
func (s *server) addProjectPage(w http.ResponseWriter, r *http.Request) {
	if !s.canAddProject() {
		s.notFound(w, r)
		return
	}
	s.renderPage(w, r, http.StatusOK, "project_add", ProjectAddPageView{})
}

// removeProjectPage is GET /projects/remove, page only like the add form.
func (s *server) removeProjectPage(w http.ResponseWriter, r *http.Request) {
	if !s.canRemoveProject() {
		s.notFound(w, r)
		return
	}
	s.renderPage(w, r, http.StatusOK, "project_remove", s.removeScreen(""))
}

// removeProjectConfirmPage is GET /projects/remove/confirm?name=, the page
// behind Remove… for a browser without JavaScript. The name must be on the
// daemon's list, and the folder shown is that list's, never the query's, so
// the address cannot make the page say anything about a project it does not
// show.
func (s *server) removeProjectConfirmPage(w http.ResponseWriter, r *http.Request) {
	if !s.canRemoveProject() {
		s.notFound(w, r)
		return
	}
	p, listed := named(s.projects(), strings.TrimSpace(r.URL.Query().Get("name")))
	if !listed {
		s.notFound(w, r)
		return
	}
	s.renderPage(w, r, http.StatusOK, "project_remove_confirm", ProjectRemoveConfirmView{Name: p.Name, Sheet: projectRemoveSheet(p.Name, p.Root)})
}

// projectChange is one add or remove that went through and that the
// overview may not show yet.
type projectChange struct {
	// Outcome is ProjectAdded or ProjectRemoved.
	Outcome string
	// Name and Root are the seam's result's, never the request's.
	Name, Root string
	At         time.Time
}

// projectPending is the changes this server made that the daemon's list may
// not show yet.
//
// It is memory and not a file on purpose. It only bridges the few seconds
// between the write and the daemon's next pass, and the overview is what
// proves the change landed: once the list shows it, the entry is dropped. A
// restart that forgets it costs a notice, never a wrong state, because the
// list itself is always read fresh. It holds no file's contents, so the
// package's "no cache" rule stands.
type projectPending struct {
	mu      sync.Mutex
	entries []projectChange
}

func newProjectPending() *projectPending { return &projectPending{} }

// record notes a change that went through and says whether it did. Only added
// and removed are noted, and only with a name to say: a notice must name the
// project, and the name comes only from the seam's result. A newer change for
// the same name replaces an older one, so a remove and a quick re-add never
// leave a notice that contradicts the other. A full list drops its oldest
// entry first.
func (p *projectPending) record(res ProjectEditResult, now time.Time) bool {
	if (res.Outcome != ProjectAdded && res.Outcome != ProjectRemoved) || res.Name == "" {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	kept := p.entries[:0]
	for _, c := range p.entries {
		if c.Name != res.Name && now.Sub(c.At) <= projectPendingTTL {
			kept = append(kept, c)
		}
	}
	kept = append(kept, projectChange{Outcome: res.Outcome, Name: res.Name, Root: res.Root, At: now})
	if n := len(kept) - projectPendingMax; n > 0 {
		kept = kept[n:]
	}
	p.entries = kept
	return true
}

// notices is the notices to show now against the daemon's list. Entries the
// list already shows, or that are too old, are dropped for good.
func (p *projectPending) notices(projects []Project, now time.Time) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	kept := p.entries[:0]
	for _, c := range p.entries {
		if changeStillPending(c, projects, now) {
			kept = append(kept, c)
		}
	}
	p.entries = kept
	return pendingNotices(kept, projects, now)
}

// changeStillPending reports whether a change is not on the list yet and not
// too old to mention. An add waits until a project with its name or its
// folder is listed (the daemon may list it under another name when the file
// was edited in between, and it is on the list either way); a remove waits
// until no project has its name.
func changeStillPending(c projectChange, projects []Project, now time.Time) bool {
	if now.Sub(c.At) > projectPendingTTL {
		return false
	}
	switch c.Outcome {
	case ProjectAdded:
		root := ""
		if c.Root != "" {
			root = filepath.Clean(c.Root)
		}
		for _, p := range projects {
			if p.Name == c.Name || (root != "" && p.Root != "" && filepath.Clean(p.Root) == root) {
				return false
			}
		}
		return true
	case ProjectRemoved:
		_, listed := named(projects, c.Name)
		return listed
	}
	return false
}

// pendingNotices is the sentences the overview shows for the changes still
// pending, oldest first. Pure: the overview's notices are a table row each in
// the tests. At most projectPendingMax are returned, the newest kept, so the
// overview can never be flooded however the list was built.
func pendingNotices(pending []projectChange, projects []Project, now time.Time) []string {
	out := []string{}
	for _, c := range pending {
		if !changeStillPending(c, projects, now) {
			continue
		}
		switch c.Outcome {
		case ProjectAdded:
			out = append(out, c.Name+" was added. It shows here when the daemon next reads the list, within a few seconds.")
		case ProjectRemoved:
			out = append(out, c.Name+" was removed. Its row goes when the daemon next reads the list, within a few seconds.")
		}
	}
	if n := len(out) - projectPendingMax; n > 0 {
		out = out[n:]
	}
	return out
}
