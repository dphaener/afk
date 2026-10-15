package web

import (
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// This file is the page's Plan a new project (T8.15, T8.15a): the owner
// types the path of a folder that already exists, or a bare project name,
// sees a confirm card naming the full path and what kind of folder it is,
// and only then starts a new-mode planning session through Deps.StartPlan,
// the same entry point the change page (plan_change.go) and the dashboard's
// new-plan form use. A name means a new folder under the profile's project
// location, made (with its git repository) by Deps.CreatePlanFolder after
// the confirm.
//
// Four rules hold here.
//
// Nothing starts without the confirm. The GET draws the form and the card and
// never reaches StartPlan; the POST reaches it only with confirm=yes in the
// body (never the query), after the folder check.
//
// Nothing is created before the confirm. The folder check
// (Deps.CheckPlanFolder) only reads, and the GET calls nothing else; the
// POST reaches Deps.CreatePlanFolder only with the confirm, and only when the
// check, asked again, still says the folder is to be created.
//
// Only a name makes a folder, and only where the profile says. The confirm
// card for a new folder posts the typed name, never the full path it showed,
// and the create seam decides again from that name, so a crafted POST cannot
// make a folder anywhere but directly under the project location. A mistyped
// path stays a refusal, never a new folder somewhere the owner did not mean.
// A create that fails part-way removes nothing: the owner is told what was
// left on disk.
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
	// Name is the typed name when the text was a bare project name (no / and
	// no ~), set whatever the kind; empty for a path. The create card posts it
	// back instead of Path, and an existing folder reached from a name gets a
	// note saying the name was found rather than made.
	Name string `json:"name,omitempty"`
}

// The kinds a PlanFolderCheck takes.
const (
	// PlanFolderRepo: an existing git repository with no plan.
	PlanFolderRepo = "repo"
	// PlanFolderFolder: an existing folder that is not a git repository;
	// planning makes it one.
	PlanFolderFolder = "folder"
	// PlanFolderCreate: the text is a bare name and its folder does not
	// exist yet; afk makes it under the project location, and makes it a git
	// repository, only after the confirm.
	PlanFolderCreate = "create"
	// PlanFolderOpen: a planning session is already open there; starting
	// resumes it.
	PlanFolderOpen = "open"
	// PlanFolderHasPlan: the folder already has a plan. Refused here: a plan
	// that exists is changed (extend, amend), never planned anew.
	PlanFolderHasPlan = "has_plan"
	// PlanFolderRefused: anything else (not found, a file, a name with
	// characters a folder name may not have, no project location).
	PlanFolderRefused = "refused"
)

// planNewPathLimit is the longest path the page sends on, in characters.
// Linux's PATH_MAX is 4096 bytes, so anything longer cannot be a folder; it is
// refused here so a pasted page of text never reaches the check.
const planNewPathLimit = 4096

// The sentences the page itself says. They are constants so the tests and
// the handler cannot drift apart.
const (
	planNewBlank      = "Type the full path of a folder, for example ~/code/shop, or a name for a new project."
	planNewHasPlan    = "This folder already has a plan. Open the project and use Change the plan to extend or amend it."
	planNewNoConfirm  = "Nothing started: planning a new project needs the confirm."
	planNewTooLong    = "That path is too long to be a folder. Type the full path of a folder, for example ~/code/shop."
	planNewUnknown    = "afk could not check this folder: the check gave an answer this page does not know. This is a fault in afk; please report it."
	planNewUnreadable = `Nothing started: the request could not be read. Send it as JSON like {"path": "~/code/shop", "confirm": true}.`
	planNewNoCreate   = "Nothing started: this page cannot make folders."
	// planNewGetNoCreate is the same refusal on the GET, where nothing was
	// asked to start yet.
	planNewGetNoCreate = "This page cannot make folders. Type the full path of a folder that exists."
	// The confirm card's headings: a create says so, since a folder will be
	// made on disk, while every other card plans in what is already there.
	planNewHeading       = "afk will plan in"
	planNewCreateHeading = "afk will create and plan in"
	// The Start planning button's label while the request runs.
	planNewLoading       = "Starting planning"
	planNewCreateLoading = "Creating the folder and starting planning"
	// planNewNoLocationHint is the Where field's hint when the profile names
	// no usable project location: a name would be refused, so it says how
	// to set one rather than naming a folder that is not there.
	planNewNoLocationHint = `Type a full path. To make new folders by name, set a project location: afk profile set "project location" ~/code`
	// planNewPathOnlyHint is the hint when the page cannot make folders at
	// all (no create seam), where offering names would only lead to a
	// refusal.
	planNewPathOnlyHint = "Type the full path of a folder."
)

// The kinds of page newPlanPageFor draws.
const (
	newPlanKindForm    = "form"
	newPlanKindConfirm = "confirm"
	newPlanKindHasPlan = "has_plan"
	// newPlanKindStarted is drawn only by the POSTs, when the session
	// started but there is a line a redirect cannot carry: the page could
	// not note the project, or there is a GitHub line (see startPlanNew).
	newPlanKindStarted = "started"
	// newPlanKindGitHub is drawn only by the POST that made a folder, when
	// gh is ready: the GitHub question (T8.15b, see plan_new_github.go).
	newPlanKindGitHub = "github"
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
	// Heading is the confirm card's heading, LoadingLabel its button's
	// label while the request runs, and Note a line under the path (an
	// existing folder reached from a name).
	Heading, LoadingLabel, Note string
	// PostPath is what the confirm form posts as path: the typed name for a
	// folder still to be created (so the POST decides again from the name,
	// never from a posted full path), the full path for every other card.
	PostPath string
	// Hint is the Where field's hint, naming the project location a name
	// lands in. Set by renderPlanNew, never by newPlanPageFor, so the pure
	// decision needs no profile.
	Hint string
	// Message is the has_plan and started pages' statement.
	Message string
	// ProjectName and ProjectHref link the has_plan page to the configured
	// project that has this folder, when there is one.
	ProjectName, ProjectHref string
	// Chat is the started page's link to the chat, when it can be opened.
	Chat string
	// Repo is the repository the GitHub question offers (account/folder)
	// and Question its legend.
	Repo, Question string
	// GitHubText is the started page's GitHub line, GitHubClass its notice
	// style and GitHubRole its ARIA role (alert for a failure, status
	// otherwise). Empty means no line.
	GitHubText, GitHubClass, GitHubRole string
}

// The badges of the confirm card, by kind.
var newPlanBadges = map[string]badgeView{
	PlanFolderRepo:   {Class: "badge--idle", Word: "Existing folder"},
	PlanFolderFolder: {Class: "badge--idle", Word: "Existing folder, becomes a git repository"},
	PlanFolderOpen:   {Class: "badge--running", Word: "Planning already open"},
	PlanFolderCreate: {Class: "badge--idle", Word: "New, empty folder"},
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
	case PlanFolderRepo, PlanFolderFolder, PlanFolderOpen, PlanFolderCreate:
		if chk.Path == "" {
			// A check that names no folder cannot be confirmed: the card
			// would ask the owner to start planning nowhere.
			v.FieldError = planNewUnknown
			return v
		}
		v.Kind, v.Path, v.Badge = newPlanKindConfirm, chk.Path, newPlanBadges[chk.Kind]
		v.Heading, v.LoadingLabel, v.PostPath = planNewHeading, planNewLoading, chk.Path
		switch {
		case chk.Kind == PlanFolderCreate:
			// The name, not the path: the POST must decide again from what
			// the owner typed, so the path on the card is never what makes
			// a folder.
			name := chk.Name
			if name == "" {
				name = text
			}
			v.Heading, v.LoadingLabel, v.PostPath = planNewCreateHeading, planNewCreateLoading, name
		case chk.Name != "":
			// The owner typed a name meaning a new project, and its folder
			// was already there: say so, or they would think it was made.
			v.Note = "A folder named " + chk.Name + " already exists in " + filepath.Dir(chk.Path) + ". afk will plan in it."
		}
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
// answering 404 until it was approved. CreatePlanFolder is deliberately not
// among them: without it the page still plans in folders that exist, and a
// name is refused at the POST.
func (s *server) canPlanNew() bool {
	return s.d.StartPlan != nil && s.d.CheckPlanFolder != nil && s.d.AddBeingPlanned != nil
}

// planNewHint is the Where field's hint. Pure. A name is only worth offering
// when the page can make folders and the profile says where; the location is
// named, never a fixed folder, because it is the owner's own setting.
func planNewHint(canCreate bool, location string) string {
	switch {
	case !canCreate:
		return planNewPathOnlyHint
	case location == "":
		return planNewNoLocationHint
	default:
		return "A name makes a new folder in " + location + "."
	}
}

// renderPlanNew draws plan_new with the Where field's hint filled in. Every
// draw of the page goes through it, so a refusal that redraws the form never
// loses the hint.
func (s *server) renderPlanNew(w http.ResponseWriter, r *http.Request, status int, v NewPlanPageView) {
	location := ""
	if s.d.PlanLocation != nil {
		location = s.d.PlanLocation()
	}
	v.Hint = planNewHint(s.d.CreatePlanFolder != nil, location)
	s.renderPage(w, r, status, "plan_new", v)
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
		if chk.Kind == PlanFolderCreate && s.d.CreatePlanFolder == nil {
			// The POST would refuse this card's Start planning, so the card
			// is never drawn.
			chk = PlanFolderCheck{Path: chk.Path, Kind: PlanFolderRefused, Name: chk.Name, Reason: planNewGetNoCreate}
		}
	}
	v := withProjectLink(newPlanPageFor(text, q.Has("path"), chk), s.projects())
	s.renderPlanNew(w, r, http.StatusOK, v)
}

// planNew is POST /plan/new and /api/plan/new: what the dashboard's new-plan
// form does, through Deps.CreatePlanFolder for a name whose folder does not
// exist yet and then Deps.StartPlan in mode new. Nothing reaches a seam
// without the confirm in the body, and neither the create, the start nor the
// list is reached for a folder the check refused. A folder made here, when gh
// is ready, gets the GitHub question first and is started by its answer
// (planNewGitHub) instead; a folder that existed is never offered one.
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
		s.renderPlanNew(w, r, http.StatusBadRequest, withProjectLink(v, s.projects()))
		return
	}

	// The full path the owner confirmed is what is started, not the typed
	// text: the card showed that path, and the entry point re-resolves it
	// either way. For a new folder it is the path the create seam made.
	path, created := chk.Path, false
	if chk.Kind == PlanFolderCreate {
		if s.d.CreatePlanFolder == nil {
			s.planNewRefuse(w, r, text, planNewNoCreate, "refused", "")
			return
		}
		// The typed text, never chk.Path: the seam decides again from the
		// name, so it alone says where a folder may be made.
		made, err := s.d.CreatePlanFolder(text)
		if err != nil {
			// Nothing is started in a folder that was half made; the
			// sentence says what was left, and nothing is removed.
			s.planNewRefuse(w, r, text, err.Error(), "create_failed", made)
			return
		}
		if made != "" {
			path = made
		}
		created = true
	}
	var gh *planNewGitHubNote
	if created && s.canAskGitHub() {
		// gh may take up to 30 seconds per call and the offer runs up to
		// three of them, past the server's 30-second write timeout. An error
		// (no connection behind the writer, as in a test) is ignored, as in
		// planApprove: the worst case is the limit we started with.
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(planNewGitHubWriteLimit))
		offer := s.d.GitHubOffer(path)
		if offer.Ask && strings.TrimSpace(offer.Repo) != "" {
			// Nothing starts yet: the answer's POST (planNewGitHub) starts
			// planning, whatever the answer, and only for a folder recorded
			// here.
			s.offered.add(path, strings.TrimSpace(offer.Repo), s.d.Now())
			s.askGitHub(w, r, path, strings.TrimSpace(offer.Repo))
			return
		}
		gh = notOfferedNote(offer)
	}
	s.startPlanNew(w, r, text, path, created, gh)
}

// startPlanNew is the tail both planNew and planNewGitHub end in: StartPlan
// in mode new for path, then notePlanned, then the answer. It is one function
// so the refusal wording, the created note and the being-planned note cannot
// drift between the two. text is what a refused page's form is redrawn with.
// gh, when not nil, is the GitHub line the answer carries (the API's github
// key, the page's line on the started view); a quiet one (declined) still
// lets the page go straight to the chat.
func (s *server) startPlanNew(w http.ResponseWriter, r *http.Request, text, path string, created bool, gh *planNewGitHubNote) {
	res := s.d.StartPlan(path, "new", "")
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
		if created {
			// The folder was made a moment ago and stays: the owner must
			// know it is there, or typing the name again would surprise
			// them with an existing folder.
			res.Reason += " The new folder " + path + " was made and is left in place."
		}
		if isAPI(r.URL.Path) {
			reason := "refused"
			if res.Open {
				reason = "session_open"
			}
			body := map[string]any{"error": res.Reason, "reason": reason, "path": path, "created": created}
			if gh != nil {
				body["github"] = gh.json
			}
			writeJSON(w, http.StatusBadRequest, body)
			return
		}
		why := res.Reason
		if gh != nil && !gh.quiet && gh.text != "" {
			// A repository may have been made a moment ago: the owner must
			// read that too, or a refused start would hide it.
			why += " " + gh.text
		}
		s.planNewRedraw(w, r, text, why)
		return
	}

	root := res.Root
	if root == "" {
		root = path
	}
	project, notice := s.notePlanned(root)
	chat := ""
	if project != "" {
		chat = chatPath(project)
	}
	if isAPI(r.URL.Path) {
		body := map[string]any{
			"path": path, "root": root, "project": project, "outcome": res.Outcome, "chat": chat, "notice": notice, "created": created,
		}
		if gh != nil {
			body["github"] = gh.json
		}
		writeJSON(w, http.StatusOK, body)
		return
	}
	if notice != "" || (gh != nil && !gh.quiet && gh.text != "") {
		// A redirect cannot carry the warning or the GitHub line, and the
		// owner must read them: the session runs, but this page may not
		// list it, or a repository was (or was not) made.
		v := NewPlanPageView{Kind: newPlanKindStarted, Path: root, Chat: chat, Notice: notice,
			Message: "Planning started in " + root + "."}
		if gh != nil && !gh.quiet {
			v.GitHubText, v.GitHubClass, v.GitHubRole = gh.text, gh.class, gh.role
		}
		s.renderPlanNew(w, r, http.StatusOK, v)
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

// planNewRefuse answers a POST the page will not carry through after the
// check: the API gets JSON with the given reason and the path (the one left
// on disk, for create_failed), the page the form again with the sentence
// above it, at 400 like every other refusal here.
func (s *server) planNewRefuse(w http.ResponseWriter, r *http.Request, text, why, reason, path string) {
	if isAPI(r.URL.Path) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": why, "reason": reason, "path": path})
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
	s.renderPlanNew(w, r, http.StatusBadRequest, v)
}
