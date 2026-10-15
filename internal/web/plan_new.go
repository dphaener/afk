package web

import (
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// This file is the page's Plan a new project (T8.15): the owner types the
// path of a folder that already exists, sees a confirm card naming the full
// path and what kind of folder it is, and only then starts a new-mode
// planning session through Deps.StartPlan, the same entry point the change
// page (plan_change.go) and the dashboard's new-plan form use.
//
// Three rules hold here.
//
// Nothing starts without the confirm. The GET draws the form and the card and
// never reaches StartPlan; the POST reaches it only with confirm=yes in the
// body (never the query), after the folder check.
//
// Nothing is created. The folder check (Deps.CheckPlanFolder) only reads, and
// the GET calls nothing else. Making a folder that does not exist is T8.15a's
// and is deliberately absent: a mistyped path must be a refusal, never a new
// folder somewhere the owner did not mean.
//
// The page decides nothing about whether planning may start. The check is
// read-only and can be out of date by the time the owner presses Start
// planning (a session opened, a plan was written), so StartPlan re-checks and
// refuses in the command's own words. The page refuses only what it can see
// for itself (no confirm, a blank or overlong path) and what the check
// already refused, so a refused folder never reaches the seam.

// PlanFolderCheck is what Deps.CheckPlanFolder says about a typed folder
// path. It is this package's own plain copy of the root's check, because this
// package never imports the root.
type PlanFolderCheck struct {
	// Path is the full path the text means (with ~ expanded), or "" when it
	// means none.
	Path string `json:"path"`
	// Kind is one of the PlanFolder constants.
	Kind string `json:"kind"`
	// Reason is the refusal as a plain sentence when Kind is refused; empty
	// otherwise.
	Reason string `json:"reason"`
}

// The kinds a PlanFolderCheck takes.
const (
	// PlanFolderRepo: an existing git repository with no plan.
	PlanFolderRepo = "repo"
	// PlanFolderFolder: an existing folder that is not a git repository;
	// planning makes it one.
	PlanFolderFolder = "folder"
	// PlanFolderOpen: a planning session is already open there; starting
	// resumes it.
	PlanFolderOpen = "open"
	// PlanFolderHasPlan: the folder already has a plan. Refused here: a plan
	// that exists is changed (extend, amend), never planned anew.
	PlanFolderHasPlan = "has_plan"
	// PlanFolderRefused: anything else (not a full path, not found, a file).
	PlanFolderRefused = "refused"
)

// planNewPathLimit is the longest path the page sends on, in characters.
// Linux's PATH_MAX is 4096 bytes, so anything longer cannot be a folder; it is
// refused here so a pasted page of text never reaches the check.
const planNewPathLimit = 4096

// The sentences the page itself says. They are constants so the tests and
// the handler cannot drift apart.
const (
	planNewBlank      = "Type the full path of a folder, for example ~/code/shop."
	planNewHasPlan    = "This folder already has a plan. Open the project and use Change the plan to extend or amend it."
	planNewNoConfirm  = "Nothing started: planning a new project needs the confirm."
	planNewTooLong    = "That path is too long to be a folder. Type the full path of a folder, for example ~/code/shop."
	planNewUnknown    = "afk could not check this folder: the check gave an answer this page does not know. This is a fault in afk; please report it."
	planNewUnreadable = `Nothing started: the request could not be read. Send it as JSON like {"path": "~/code/shop", "confirm": true}.`
)

// The kinds of page newPlanPageFor draws.
const (
	newPlanKindForm    = "form"
	newPlanKindConfirm = "confirm"
	newPlanKindHasPlan = "has_plan"
	// newPlanKindStarted is drawn only by the POST, when the session started
	// but the page could not note the project (see planNew).
	newPlanKindStarted = "started"
)

// NewPlanPageView is what plan_new.html draws. Every decision is made in
// newPlanPageFor (and planNew for the started page), so the template only
// chooses between Kinds.
type NewPlanPageView struct {
	Kind string
	// Text is what the owner typed, kept in the field after a refusal.
	Text string
	// FieldError is the form's refusal, under the field.
	FieldError string
	// Notice is a refusal or a warning shown above the form or card.
	Notice string
	// Path is the folder's full path and Badge what kind of folder it is,
	// on the confirm card.
	Path  string
	Badge badgeView
	// Message is the has_plan and started pages' statement.
	Message string
	// ProjectName and ProjectHref link the has_plan page to the configured
	// project that has this folder, when there is one.
	ProjectName, ProjectHref string
	// Chat is the started page's link to the chat, when it can be opened.
	Chat string
}

// The badges of the confirm card, by kind.
var newPlanBadges = map[string]badgeView{
	PlanFolderRepo:   {Class: "badge--idle", Word: "Existing folder"},
	PlanFolderFolder: {Class: "badge--idle", Word: "Existing folder, becomes a git repository"},
	PlanFolderOpen:   {Class: "badge--running", Word: "Planning already open"},
}

// newPlanPageFor decides what the Plan a new project page says. Pure. given
// is whether the request carried a path at all: a first visit has none and
// is not told it left the field blank, while a submitted blank is. chk is the
// folder check's answer; it is ignored (and the caller must not have asked
// for it) when the text is blank. Each branch is a test row.
func newPlanPageFor(text string, given bool, chk PlanFolderCheck) NewPlanPageView {
	text = strings.TrimSpace(text)
	v := NewPlanPageView{Kind: newPlanKindForm, Text: text}
	switch {
	case text == "":
		if given {
			v.FieldError = planNewBlank
		}
		return v
	case utf8.RuneCountInString(text) > planNewPathLimit:
		v.FieldError = planNewTooLong
		return v
	}
	switch chk.Kind {
	case PlanFolderRepo, PlanFolderFolder, PlanFolderOpen:
		if chk.Path == "" {
			// A check that names no folder cannot be confirmed: the card
			// would ask the owner to start planning nowhere.
			v.FieldError = planNewUnknown
			return v
		}
		v.Kind, v.Path, v.Badge = newPlanKindConfirm, chk.Path, newPlanBadges[chk.Kind]
	case PlanFolderHasPlan:
		v.Kind, v.Path, v.Message = newPlanKindHasPlan, chk.Path, planNewHasPlan
	case PlanFolderRefused:
		v.FieldError = chk.Reason
		if v.FieldError == "" {
			v.FieldError = "afk cannot plan in this folder, and no reason was given. Type the full path of a folder that exists."
		}
	default:
		v.FieldError = planNewUnknown
	}
	return v
}

// withProjectLink links a has_plan page to the configured project whose
// folder that is, so "Open the project" is one press. Pure. A folder no
// configured project has gets no link: its plan may be on the review list or
// in a folder afk does not build, and a link to a project page that answers
// 404 would be worse than none.
func withProjectLink(v NewPlanPageView, configured []Project) NewPlanPageView {
	if v.Kind != newPlanKindHasPlan || v.Path == "" {
		return v
	}
	if p, ok := projectWithRoot(configured, v.Path); ok {
		v.ProjectName, v.ProjectHref = p.Name, "/p/"+url.PathEscape(p.Name)
	}
	return v
}

// projectWithRoot finds the configured project whose folder is root.
func projectWithRoot(ps []Project, root string) (Project, bool) {
	root = filepath.Clean(root)
	for _, p := range ps {
		if p.Root != "" && filepath.Clean(p.Root) == root {
			return p, true
		}
	}
	return Project{}, false
}

// canPlanNew reports whether the page offers Plan a new project at all. All
// three seams are needed: without the check there is no confirm card to draw,
// without the start nothing can be planned, and without the being-planned
// list a project started here would vanish from the page with its chat
// answering 404 until it was approved.
func (s *server) canPlanNew() bool {
	return s.d.StartPlan != nil && s.d.CheckPlanFolder != nil && s.d.AddBeingPlanned != nil
}

// planNewPage is GET /plan/new: the Where form, or with ?path= the confirm
// card. It is not in routes: it has no JSON form, so it must not get an /api
// twin. It calls only the read-only folder check, never StartPlan and never
// AddBeingPlanned.
func (s *server) planNewPage(w http.ResponseWriter, r *http.Request) {
	if !s.canPlanNew() {
		s.notFound(w, r)
		return
	}
	q := r.URL.Query()
	text := strings.TrimSpace(q.Get("path"))
	var chk PlanFolderCheck
	if text != "" && utf8.RuneCountInString(text) <= planNewPathLimit {
		chk = s.d.CheckPlanFolder(text)
	}
	v := withProjectLink(newPlanPageFor(text, q.Has("path"), chk), s.projects())
	s.renderPage(w, r, http.StatusOK, "plan_new", v)
}

// planNew is POST /plan/new and /api/plan/new: what the dashboard's new-plan
// form does for a folder that exists, through Deps.StartPlan in mode new.
// Nothing reaches a seam without the confirm in the body, and neither the
// start nor the list is reached for a folder the check refused.
func (s *server) planNew(w http.ResponseWriter, r *http.Request) {
	if !s.canPlanNew() {
		s.notFound(w, r)
		return
	}
	b, err := readPostBody(r)
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		s.tooLarge(w, r)
		return
	}
	if err != nil || !b.confirmed() {
		// Best effort: the page redraws with whatever was typed.
		t, _ := bodyField(b, "path")
		s.planNewBad(w, r, strings.TrimSpace(t), planNewNoConfirm)
		return
	}
	text, ok := bodyField(b, "path")
	if !ok {
		s.planNewBad(w, r, "", planNewUnreadable)
		return
	}
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) > planNewPathLimit {
		v := newPlanPageFor(text, true, PlanFolderCheck{})
		s.planNewBad(w, r, text, "Nothing started: "+v.FieldError)
		return
	}

	chk := s.d.CheckPlanFolder(text)
	if v := newPlanPageFor(text, true, chk); v.Kind != newPlanKindConfirm {
		if isAPI(r.URL.Path) {
			reason, why := "refused", v.FieldError
			if v.Kind == newPlanKindHasPlan {
				reason, why = "has_plan", v.Message
			}
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": why, "reason": reason, "path": chk.Path})
			return
		}
		s.renderPage(w, r, http.StatusBadRequest, "plan_new", withProjectLink(v, s.projects()))
		return
	}

	// The full path the owner confirmed is what is started, not the typed
	// text: the card showed that path, and the entry point re-resolves it
	// either way.
	res := s.d.StartPlan(chk.Path, "new", "")
	switch res.Outcome {
	case PlanStartStarted, PlanStartResumed:
	case PlanStartRefused:
	default:
		res = PlanStartResult{Outcome: PlanStartRefused, Reason: "Starting the planning session gave an answer this page does not know. This is a fault in afk; please report it. Run afk plan status in the folder to see where the plan is now."}
	}
	if res.Outcome == PlanStartRefused {
		if res.Reason == "" {
			res.Reason = "Nothing was started, and no reason was given. Run afk plan status in the folder to see where the plan is now."
		}
		if isAPI(r.URL.Path) {
			reason := "refused"
			if res.Open {
				reason = "session_open"
			}
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": res.Reason, "reason": reason, "path": chk.Path})
			return
		}
		s.planNewRedraw(w, r, text, res.Reason)
		return
	}

	root := res.Root
	if root == "" {
		root = chk.Path
	}
	project, notice := s.notePlanned(root)
	chat := ""
	if project != "" {
		chat = chatPath(project)
	}
	if isAPI(r.URL.Path) {
		writeJSON(w, http.StatusOK, map[string]any{
			"path": chk.Path, "root": root, "project": project, "outcome": res.Outcome, "chat": chat, "notice": notice,
		})
		return
	}
	if notice != "" {
		// A redirect cannot carry the warning, and the owner must read it:
		// the session runs, but this page may not list it.
		v := NewPlanPageView{Kind: newPlanKindStarted, Path: root, Chat: chat, Notice: notice,
			Message: "Planning started in " + root + "."}
		s.renderPage(w, r, http.StatusOK, "plan_new", v)
		return
	}
	w.Header().Set("Location", chat+"?done="+url.QueryEscape(res.Outcome))
	w.WriteHeader(http.StatusSeeOther)
}

// notePlanned adds the folder to the being-planned list after a start, and
// says which project name its chat is under, and a warning when the list
// could not be written.
//
// A configured project with that folder wins the name: beingPlanned drops a
// list entry whose folder a configured project has, so the chat is reached
// under the configured name and the list's own name would answer 404. The
// list is still written, so the entry is there if the project is later
// removed from config.toml while still being planned.
func (s *server) notePlanned(root string) (project, notice string) {
	name, err := s.d.AddBeingPlanned(root)
	if p, ok := projectWithRoot(s.projects(), root); ok {
		return p.Name, ""
	}
	if err != nil {
		return "", "The planning session is running, but afk could not add the project to this page's list (" + err.Error() + "), so it will not show on the overview until its plan is approved. Run afk plan status in " + root + " to follow it."
	}
	if name == "" {
		return "", "The planning session is running, but afk gave the project no name for this page, so it will not show on the overview until its plan is approved. Run afk plan status in " + root + " to follow it."
	}
	return name, ""
}

// planNewBad answers a POST the page itself will not send on: the API gets
// JSON with the reason bad_request, the page the form again with the
// sentence above it.
func (s *server) planNewBad(w http.ResponseWriter, r *http.Request, text, why string) {
	if isAPI(r.URL.Path) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": why, "reason": "bad_request"})
		return
	}
	s.planNewRedraw(w, r, text, why)
}

// planNewRedraw answers a refused page POST with the Where form at 400: the
// sentence in a notice above it and the typed path kept, so a refusal costs
// the owner no typing. It is the form and not the card again, because the
// card's Start planning would only send the same refused request.
func (s *server) planNewRedraw(w http.ResponseWriter, r *http.Request, text, why string) {
	v := NewPlanPageView{Kind: newPlanKindForm, Text: text, Notice: why}
	s.renderPage(w, r, http.StatusBadRequest, "plan_new", v)
}
