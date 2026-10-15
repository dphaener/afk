package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/dphaener/afk/internal/plansession"
)

// This file is the page's way to start a planning session that changes a
// plan that is already being built (T8.14): the web twin of `afk plan
// extend`, `afk plan amend "<what>"` and `afk plan reconcile FILE`.
//
// The package starts nothing itself. Deps.StartPlan is the root package's
// print-free entry point (the one the dashboard uses), which re-reads the
// project, the plan and any open planning session and refuses in the
// command's own words. The handler therefore checks only what the request
// itself can get wrong: the confirm, the mode and the text. It never decides
// whether a session may start, because a second copy of that rule would drift
// from the first.
//
// There is no `yes` here by construction: Amend on the command line may skip
// its own confirm with --yes, but the page has its own confirm and sends no
// such request, so nothing on this page writes .afk/plan/yes.

// PlanStartResult is what Deps.StartPlan reports. It is this package's own
// plain copy of the root's planning-start result, because this package never
// imports the root.
type PlanStartResult struct {
	// Outcome is one of the PlanStart constants.
	Outcome string
	// Reason is the refusal in the command's own words; empty otherwise.
	Reason string
	// Open is true when the refusal is that a planning session is already
	// open; OpenMode and OpenPhase then say which.
	Open      bool
	OpenMode  string
	OpenPhase string
	// Root is the project folder the entry point resolved and started in
	// (symlinks resolved for mode new), or "" when it did not say. Plan a
	// new project records the project under it, so the being-planned list
	// names the folder the session really runs in, not the text typed.
	Root string `json:"root"`
}

// The outcomes a PlanStartResult takes.
const (
	// PlanStartStarted: a planning session was started now.
	PlanStartStarted = "started"
	// PlanStartResumed: a planning session was already there and is now
	// running again.
	PlanStartResumed = "resumed"
	// PlanStartRefused: nothing was started.
	PlanStartRefused = "refused"
)

// planChangeTextLimit is the longest amend sentence or reconcile path the
// page accepts, in characters. It is the same limit the new-session
// description has (T8.10), so the pages agree on what "too long" means.
const planChangeTextLimit = 5000

// The modes the page can start. "new" is deliberately not one of them: a new
// plan is started from the dashboard's new-plan form, and starting one here
// would make a project that already has a plan a second one.
const (
	changeExtend    = "extend"
	changeAmend     = "amend"
	changeReconcile = "reconcile"
)

// validChangeMode reports whether mode is one the page can start.
func validChangeMode(mode string) bool {
	return mode == changeExtend || mode == changeAmend || mode == changeReconcile
}

// PlanChangeView is the API's answer to a change. Every key is always
// present, so a reader does not have to test for them.
type PlanChangeView struct {
	Project     string `json:"project"`
	Mode        string `json:"mode"`
	Outcome     string `json:"outcome"`
	Text        string `json:"text"`
	Chat        string `json:"chat"`
	SessionOpen bool   `json:"session_open"`
	OpenMode    string `json:"open_mode"`
	OpenPhase   string `json:"open_phase"`
}

// changeRefusal checks the mode and text of a change before the seam is
// asked, and returns the sentence to refuse with, or "" when they are fine.
// Pure. text is already trimmed. Each branch is a test row.
func changeRefusal(mode, text string) string {
	switch {
	case !validChangeMode(mode):
		return "Nothing started: " + mode + " is not a way to change a plan. Use extend, amend or reconcile."
	case mode == changeAmend && text == "":
		return "Nothing started: say what to change in the plan."
	case mode == changeReconcile && text == "":
		return "Nothing started: name the document to reconcile the plan with, as a path inside the project."
	case utf8.RuneCountInString(text) > planChangeTextLimit:
		return "Nothing started: that is too long. Keep it under 5000 characters."
	}
	return ""
}

// planChange is the change POST: what `afk plan extend|amend|reconcile`
// does, through Deps.StartPlan, with the project's own root. Nothing reaches
// the seam without the confirm in the body, and the root, the mode list and
// the confirm never come from the query.
func (s *server) planChange(w http.ResponseWriter, r *http.Request) {
	p, ok := s.findProject(w, r)
	if !ok {
		return
	}
	if s.d.StartPlan == nil {
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
		m, _ := bodyField(b, "mode")
		t, _ := bodyField(b, "text")
		s.changeBad(w, r, p, strings.TrimSpace(m), strings.TrimSpace(t), "Nothing changed: changing the plan needs the confirm.")
		return
	}
	mode, okMode := bodyField(b, "mode")
	text, okText := bodyField(b, "text")
	if !okMode || !okText {
		s.changeBad(w, r, p, "", "", `Nothing started: the request could not be read. Send it as JSON like {"mode": "amend", "text": "Add a CSV export task.", "confirm": true}.`)
		return
	}
	mode, text = strings.TrimSpace(mode), strings.TrimSpace(text)
	if mode == changeExtend {
		// Extend takes no text; whatever came with it is not sent.
		text = ""
	}
	if why := changeRefusal(mode, text); why != "" {
		s.changeBad(w, r, p, mode, text, why)
		return
	}
	res := s.d.StartPlan(p.Root, mode, text)
	switch res.Outcome {
	case PlanStartStarted, PlanStartResumed:
	case PlanStartRefused:
	default:
		res = PlanStartResult{Outcome: PlanStartRefused, Reason: "Starting the planning session gave an answer this page does not know. This is a fault in afk; please report it. Run afk plan status in the project folder to see where the plan is now."}
	}
	if res.Outcome == PlanStartRefused && res.Reason == "" {
		res.Reason = "Nothing was started, and no reason was given. Run afk plan status in the project folder to see where the plan is now."
	}
	refused := res.Outcome == PlanStartRefused
	if isAPI(r.URL.Path) {
		status := http.StatusOK
		view := map[string]any{
			"project": p.Name, "mode": mode, "outcome": res.Outcome, "text": text,
			"chat": chatPath(p.Name), "session_open": res.Open, "open_mode": res.OpenMode, "open_phase": res.OpenPhase,
		}
		if refused {
			status = http.StatusBadRequest
			reason := "refused"
			if res.Open {
				reason = "session_open"
			}
			view["error"] = res.Reason
			view["reason"] = reason
		}
		writeJSON(w, status, view)
		return
	}
	if refused {
		s.changeRedraw(w, r, p, mode, text, res.Reason, &res)
		return
	}
	w.Header().Set("Location", chatPath(p.Name)+"?done="+url.QueryEscape(res.Outcome))
	w.WriteHeader(http.StatusSeeOther)
}

// changeBad answers a change the page itself will not send to its seam: the
// API gets JSON with the reason bad_request, the page the change page again
// with the sentence above it.
func (s *server) changeBad(w http.ResponseWriter, r *http.Request, p Project, mode, text, why string) {
	if isAPI(r.URL.Path) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": why, "reason": "bad_request"})
		return
	}
	s.changeRedraw(w, r, p, mode, text, why, nil)
}

// changeRedraw answers a refused page POST with the change page at 400: the
// refusal's sentence in a notice above the form or card, the typed text kept,
// and, when the seam said a session is already open, the link to its chat. A
// refusal costs the owner no typing and no trip back to find the button.
// refused is the seam's answer, or nil when the page refused before asking.
func (s *server) changeRedraw(w http.ResponseWriter, r *http.Request, p Project, mode, text, why string, refused *PlanStartResult) {
	st := s.changeState(p)
	if refused != nil && refused.Open {
		// The seam read the session a moment ago, which is fresher than the
		// page's own read.
		st.Open, st.OpenMode, st.OpenPhase = true, refused.OpenMode, refused.OpenPhase
	}
	v := changePageFor(p.Name, mode, text, true, st)
	// The unknown-mode page already says the sentence; saying it twice reads
	// as two problems.
	if why != v.Message {
		v.Notice = why
	}
	s.renderPage(w, r, http.StatusBadRequest, "plan_change", v)
}

// changeState is what the change page needs to know about the project.
type changeState struct {
	// HasPlan is whether the project has a plan to change.
	HasPlan bool
	// Open is whether a planning session is open (not finished), and
	// OpenMode and OpenPhase say which.
	Open                bool
	OpenMode, OpenPhase string
}

// changeState reads the same two things the project page reads: the plan and
// the planning session's state file. A state file that cannot be read is not
// called open; the seam reads it again and refuses in its own words.
func (s *server) changeState(p Project) changeState {
	st := changeState{HasPlan: true}
	if s.d.ReadPlan != nil {
		_, _, err := s.d.ReadPlan(p.Root)
		st.HasPlan = err == nil
	}
	if ps, ok, err := plansession.ReadState(p.Root); err == nil && ok && !plansession.Finished(ps) {
		st.Open, st.OpenMode, st.OpenPhase = true, ps.Mode, ps.Phase
	}
	return st
}

// The kinds of change page changePageFor draws.
const (
	changeKindNone    = "none"
	changeKindOpen    = "open"
	changeKindUnknown = "unknown"
	changeKindForm    = "form"
	changeKindConfirm = "confirm"
)

// ChangePageView is what plan_change.html draws. Every decision is made in
// changePageFor, so the template only chooses between Kinds.
type ChangePageView struct {
	Project, Mode, Text string
	Kind                string
	// Title is the heading; Lede the line under a confirm's heading.
	Title, Lede string
	// Message is the plain statement of the none, open and unknown pages.
	Message string
	// Notice is a refusal's sentence, shown above the form or card.
	Notice string
	// Label, Hint, Textarea and FieldError describe the one field of a form.
	Label, Hint, FieldError string
	Textarea                bool
	// Action is where the confirm card posts, Chat the chat's address and
	// Back the project's.
	Action, Chat, Back string
}

// changePageFor decides what the change page says. Pure. textGiven is
// whether the request carried a text at all: a first visit has none and is
// not told it left the field blank, while a submitted blank is. Each branch
// is a test row; the order matters, since a project with no plan has nothing
// to open a chat for, and an open session wins over every form because a
// second session cannot start.
func changePageFor(project, mode, text string, textGiven bool, st changeState) ChangePageView {
	text = strings.TrimSpace(text)
	v := ChangePageView{
		Project: project, Mode: mode, Text: text,
		Action: planBase(project) + "/change", Chat: chatPath(project), Back: "/p/" + url.PathEscape(project),
	}
	switch {
	case !st.HasPlan:
		v.Kind, v.Title, v.Message = changeKindNone, "Change the plan", "There is no plan to change yet."
		return v
	case st.Open:
		v.Kind, v.Title = changeKindOpen, "Change the plan"
		v.Message = "A planning session is already open (" + st.OpenMode + ", " + st.OpenPhase + ")."
		return v
	case !validChangeMode(mode):
		v.Kind, v.Title, v.Message = changeKindUnknown, "Change the plan", changeRefusal(mode, "")
		return v
	}
	switch mode {
	case changeExtend:
		v.Text = ""
		v.Kind, v.Title = changeKindConfirm, "Extend the plan for "+project+"?"
		v.Lede = "A planning agent reads the plan and asks you what to add. Its chat opens next."
		return v
	case changeAmend:
		v.Label, v.Textarea = "What should change?", true
	default:
		v.Label, v.Hint = "Document to reconcile with", "A path inside the project, such as docs/design-system.md."
	}
	if text == "" {
		v.Kind = changeKindForm
		v.Title = "Amend the plan for " + project
		if mode == changeReconcile {
			v.Title = "Reconcile the plan for " + project
		}
		if textGiven {
			v.FieldError = "Say what to change."
			if mode == changeReconcile {
				v.FieldError = "Name a document."
			}
		}
		return v
	}
	v.Kind = changeKindConfirm
	if mode == changeAmend {
		v.Title = "Amend the plan for " + project + "?"
		v.Lede = "A planning agent reads what you wrote, makes the change with you and opens its chat next."
	} else {
		v.Title = "Reconcile the plan for " + project + " with " + text + "?"
		v.Lede = "A planning agent reads that document and asks you what to change in the plan. Its chat opens next."
	}
	return v
}

// planChangePage is the no-JS page behind the Change the plan rows. It is
// not in routes: it has no JSON form, so it must not get an /api twin. It
// never calls Deps.StartPlan; only the POST does, behind the confirm.
func (s *server) planChangePage(w http.ResponseWriter, r *http.Request) {
	p, ok := s.findProject(w, r)
	if !ok {
		return
	}
	if s.d.StartPlan == nil {
		s.notFound(w, r)
		return
	}
	q := r.URL.Query()
	v := changePageFor(p.Name, strings.TrimSpace(q.Get("mode")), q.Get("text"), q.Has("text"), s.changeState(p))
	status := http.StatusOK
	if v.Kind == changeKindUnknown {
		status = http.StatusBadRequest
	}
	s.renderPage(w, r, status, "plan_change", v)
}
