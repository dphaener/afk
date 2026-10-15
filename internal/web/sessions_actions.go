package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// This file is the session actions of the Sessions screens: Clean, Restart
// and Label on a session's screen, and Start a new autonomous session… on
// the list (docs/design-system.md §3 "Sessions list and session"). Each one
// asks first, in the dashboard's words, and only a POST that carries the
// confirm reaches its Deps seam.
//
// The package still never touches a session itself: the seams are the root
// package's print-free entry points beside `afk clean` and `afk new
// --autonomous`, and the dashboard's restart and label writer. The handlers
// here decide only what is offered, what the confirm says and how an answer
// is shown. A request names a session and nothing else; which folder it
// lives in is looked up again behind the seam, so no request can point an
// action at a folder of its choosing.
//
// Only autonomous sessions are started from here. An attended session asks
// its owner things in a terminal this page cannot show, and a one-off is
// nothing but a terminal, so neither is offered.

// The actions a session's screen can offer, as they appear in addresses.
const (
	actionClean   = "clean"
	actionRestart = "restart"
	actionLabel   = "label"
	actionNew     = "new"
)

// sessionActionWords are the actions in a word, for the phone bar's title on
// their no-JS confirm pages.
var sessionActionWords = map[string]string{
	actionClean: "Clean", actionRestart: "Restart", actionLabel: "Label", actionNew: "New session",
}

// maxLabelChars is the longest label the page sends, in characters: the
// dashboard's label editor holds no more.
const maxLabelChars = 128

// maxDescriptionChars is the longest description the page sends, in
// characters: the dashboard's new-session form holds no more.
const maxDescriptionChars = 5000

// sessionNamePattern is the rule a new session's name must meet. It is
// checked here only so the page can say what is wrong in its own words
// before anything starts; the root checks the name again (with the reserved
// names and the existing branches, which this package cannot see), so a
// name that passes here can still be refused there.
var sessionNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// The ids of the confirm sheets' dialogs, which the Actions rows' and the
// new-session form's data-confirm attributes name.
const (
	cleanSheetID   = "confirm-clean"
	restartSheetID = "confirm-restart"
	labelSheetID   = "confirm-label"
	newSheetID     = "confirm-new"
)

// sessionOffer is which actions a session's row offers, and why one is not.
// It is the pure decision behind the Actions rows, the no-JS confirm pages
// and the POSTs, so a button the page does not draw is also a POST it
// refuses: an address typed by hand cannot do what the page would not offer.
type sessionOffer struct {
	Restart, Label, Clean bool
	// RestartWhy and LabelWhy say, in a sentence the owner can act on, why
	// the action is not offered; "" when it is.
	RestartWhy, LabelWhy string
}

// offersFor decides what a row offers. The order of the restart checks
// matters only for which sentence is shown; each is a test row.
//
// Restart: only a planned session (not a one-off, not the planning session,
// not a worktree problem, not a stranded entry) whose host is stopped, or
// running with its agent gone quiet. A one-off or the planning session
// would come back as an attended session the owner cannot talk to here, and
// a healthy host has nothing to restart: restarting it would only cut its
// agent off mid-work.
//
// Label: every row that is a session. Clean: every row, a worktree problem
// included, because tidying one up is exactly what clean is for; what clean
// refuses is decided behind the seam with `afk clean`'s own rules.
func offersFor(s SessionView) sessionOffer {
	o := sessionOffer{Label: true, Clean: true}
	switch {
	case s.Problem != "":
		o.RestartWhy = "This is a problem with a worktree, not a session, so there is nothing to restart. Clean can tidy it up."
	case s.Planning:
		o.RestartWhy = "The planning session is restarted from afk dash, not from this page. You can talk to it in the planning chat."
	case s.Scratch:
		o.RestartWhy = "A one-off session is restarted from afk dash, not from this page."
	case s.Stranded:
		o.RestartWhy = "This session's folder is not in a worktree of its own, so there is nowhere to restart it. Clean can tidy it up."
	case s.HostRunning && !s.Wedged:
		o.RestartWhy = "Its host is running and its agent is working, so there is nothing to restart."
	default:
		o.Restart = true
	}
	if s.Problem != "" {
		o.Label = false
		o.LabelWhy = "This is a problem with a worktree, not a session, so it has no label to set."
	}
	return o
}

// sheetLine is one sentence of a confirm sheet. Fill, when set, is the same
// sentence with {name} and {description} where the form's values go:
// confirm.js puts the typed values in, so the script fills in words the
// server wrote and never words of its own.
type sheetLine struct {
	Text, Fill string
}

// sheetField is a hidden field the confirm form posts beside the confirm.
// Fill marks one confirm.js copies from the new-session form.
type sheetField struct {
	Name, Value string
	Fill        bool
}

// sessionSheet is one session action's confirm: the dialog on the page and
// the no-JS page say the same thing because both are drawn from it.
type sessionSheet struct {
	// ID is the dialog's id; Action the address word ("clean", ...).
	ID, Action string
	// Title is the question naming the session; TitleFill its form for
	// confirm.js (see sheetLine).
	Title, TitleFill string
	// Body is what happens, one sentence to a paragraph.
	Body []sheetLine
	// Quote is text the owner typed, shown as it is (the new session's
	// description); QuoteFill marks it as filled in by confirm.js.
	Quote     string
	QuoteFill bool
	// Button is the action button's label and Loading its "-ing" form shown
	// while the form is sent; Danger draws it in the danger colour.
	Button, Loading string
	Danger          bool
	// Path is the POST address and Page the no-JS confirm page's address.
	Path, Page string
	// Safe is the harmless button's label.
	Safe   string
	Fields []sheetField
	// LabelInput puts the label field in the sheet, holding LabelValue, with
	// Hint under it.
	LabelInput bool
	LabelValue string
	Hint       string
}

// cleanTitle is the clean confirm's question, the dashboard's
// confirmCleanLine: the session and, in brackets, its phase or what is
// wrong with it. A one-off has no phase at all, so it is named as a one-off
// instead (the dashboard's scratch dialog), and a worktree problem names the
// problem: "phase: unknown" is the verdict a session with an unreadable
// status.json wears, and would name the wrong fault.
func cleanTitle(s SessionView) string {
	switch {
	case s.Problem != "":
		return fmt.Sprintf("Clean session %q (worktree problem)?", s.Name)
	case s.Scratch:
		return fmt.Sprintf("Clean one-off session %q?", s.Name)
	case s.Phase == "":
		return fmt.Sprintf("Clean session %q (phase: unknown)?", s.Name)
	}
	return fmt.Sprintf("Clean session %q (phase: %s)?", s.Name, s.Phase)
}

// cleanBody is what clean really removes for this kind of row, in the
// dashboard's sentences (internal/ui's confirmClean*Consequence), because a
// destructive confirm must not promise what the clean will not keep: a
// worktree fault has nothing to archive, and a stranded entry's clean
// touches no worktree at all. The design system's sample sentence differs
// from what clean does and is deliberately not used.
func cleanBody(s SessionView) []sheetLine {
	var first string
	switch {
	case s.Problem != "":
		first = "Worktree goes; nothing archived. The branch is kept."
	case s.Scratch:
		first = "Stops the host and removes the session directory."
	case s.Stranded:
		first = "Session dir archived and removed; worktree kept."
	default:
		first = "Worktree goes; archive kept. The branch is kept."
	}
	out := []sheetLine{{Text: first}}
	if s.HostRunning && !s.Scratch && s.Problem == "" {
		out = append(out, sheetLine{Text: "Its host is running, so it is stopped first."})
	}
	return out
}

// sessionConfirmPath is the no-JS confirm page of one action on a session.
func sessionConfirmPath(project, name, action string) string {
	return sessionBase(project, name) + "/confirm/" + action
}

// cleanSheetFor builds the clean confirm for a row. Pure. The phase shown is
// posted with it, so a clean the owner agreed to for a finished session is
// refused if the session has moved on since.
func cleanSheetFor(project string, s SessionView) sessionSheet {
	return sessionSheet{
		ID: cleanSheetID, Action: actionClean,
		Title: cleanTitle(s), Body: cleanBody(s),
		Button: "Clean session", Loading: "Cleaning", Danger: true,
		Path: sessionBase(project, s.Name) + "/clean", Page: sessionConfirmPath(project, s.Name, actionClean),
		Safe: "Keep it", Fields: []sheetField{{Name: "phase", Value: s.Phase}},
	}
}

// restartSheetFor builds the restart confirm. Pure. A wedged host's restart
// replaces its child through the host's one client slot, so whoever is
// attached is dropped: the dashboard says so and so does this. A stopped
// host has no client to drop.
func restartSheetFor(project string, s SessionView) sessionSheet {
	body := "Starts its host again; the agent resumes from its files."
	if s.HostRunning {
		body = "Replaces its child process and disconnects any attached client. The agent resumes its conversation."
	}
	return sessionSheet{
		ID: restartSheetID, Action: actionRestart,
		Title: fmt.Sprintf("Restart %q?", s.Name), Body: []sheetLine{{Text: body}},
		Button: "Restart", Loading: "Restarting",
		Path: sessionBase(project, s.Name) + "/restart", Page: sessionConfirmPath(project, s.Name, actionRestart),
		Safe: "Leave it",
	}
}

// labelSheetFor builds the label editor, the dashboard's "Label for <name>:"
// with the current label filled in. Pure.
func labelSheetFor(project string, s SessionView) sessionSheet {
	return sessionSheet{
		ID: labelSheetID, Action: actionLabel,
		Title:  "Label for " + s.Name + ":",
		Button: "Save label", Loading: "Saving",
		Path: sessionBase(project, s.Name) + "/label", Page: sessionConfirmPath(project, s.Name, actionLabel),
		Safe: "Leave it", LabelInput: true, LabelValue: s.Label, Hint: "blank clears it",
	}
}

// newTitle and newBody are the new-session confirm's words with {name} where
// the name goes; newSheetFor fills them for the no-JS page and hands them to
// confirm.js for the dialog.
const (
	newTitle     = `Start autonomous session "{name}"?`
	newBodyRun   = "It works on its own, from plan to reviewed commits on branch {name}, with nobody asked along the way."
	newBodyAfter = "Its terminal opens only from afk dash or afk attach."
)

// newSheetFor builds the new-session confirm. Pure. With an empty name it is
// the dialog on the list, whose words confirm.js fills in from the form;
// with a name it is the no-JS page's, already filled.
func newSheetFor(project, name, description string) sessionSheet {
	fill := func(t string) string { return strings.ReplaceAll(t, "{name}", name) }
	sh := sessionSheet{
		ID: newSheetID, Action: actionNew,
		Title: fill(newTitle), TitleFill: newTitle,
		Body:   []sheetLine{{Text: fill(newBodyRun), Fill: newBodyRun}, {Text: newBodyAfter}},
		Quote:  description,
		Button: "Start session", Loading: "Starting",
		Path: sessionsBase(project), Page: sessionsBase(project) + "/new",
		Safe:   "Don't start",
		Fields: []sheetField{{Name: "name", Value: name, Fill: true}, {Name: "description", Value: description, Fill: true}},
	}
	if name == "" {
		sh.QuoteFill = true
	}
	return sh
}

// actionRow is one row of the session screen's Actions list: a link to the
// no-JS confirm page that opens the sheet instead when the script runs.
type actionRow struct {
	Label, Meta, Href, Sheet string
	Danger                   bool
}

// sessionActions is what the session screen draws for the actions: the rows
// and their sheets, in the design's order (Restart, Label, Clean).
type sessionActions struct {
	Rows   []actionRow
	Sheets []sessionSheet
}

// actionSeams says which session action seams the server was given; an
// action without one is not drawn.
type actionSeams struct {
	Clean, Restart, Label, New bool
}

func (s *server) seams() actionSeams {
	return actionSeams{
		Clean: s.d.CleanSession != nil, Restart: s.d.RestartSession != nil,
		Label: s.d.SetSessionLabel != nil, New: s.d.NewSession != nil,
	}
}

// sessionActionsFor lays out a session's Actions. Pure: what is offered comes
// from offersFor and what the server can do from seams.
func sessionActionsFor(project string, s SessionView, have actionSeams) sessionActions {
	o := offersFor(s)
	var a sessionActions
	if have.Restart && o.Restart {
		sh := restartSheetFor(project, s)
		meta := "Its host is not running."
		if s.HostRunning {
			meta = "Its agent has gone quiet."
		}
		a.Rows = append(a.Rows, actionRow{Label: "Restart…", Meta: meta, Href: sh.Page, Sheet: sh.ID})
		a.Sheets = append(a.Sheets, sh)
	}
	if have.Label && o.Label {
		sh := labelSheetFor(project, s)
		meta := "None set."
		if s.Label != "" {
			meta = "Now: " + s.Label
		}
		a.Rows = append(a.Rows, actionRow{Label: "Label…", Meta: meta, Href: sh.Page, Sheet: sh.ID})
		a.Sheets = append(a.Sheets, sh)
	}
	if have.Clean && o.Clean {
		sh := cleanSheetFor(project, s)
		a.Rows = append(a.Rows, actionRow{Label: "Clean…", Meta: sh.Body[0].Text, Href: sh.Page, Sheet: sh.ID, Danger: true})
		a.Sheets = append(a.Sheets, sh)
	}
	return a
}

// newSessionForm is the list's Start a new autonomous session… card and the
// no-JS page's first step: the form that GETs the confirm page, which
// confirm.js turns into opening Sheet instead.
type newSessionForm struct {
	// Page is where the form goes without the script; SheetID the dialog it
	// opens with it.
	Page, SheetID     string
	Name, Description string
}

// newSessionPart is what the list draws for starting a session: the card's
// form and the confirm sheet it opens.
type newSessionPart struct {
	Form  newSessionForm
	Sheet sessionSheet
}

func newSessionPartFor(project string) *newSessionPart {
	return &newSessionPart{
		Form:  newSessionForm{Page: sessionsBase(project) + "/new", SheetID: newSheetID},
		Sheet: newSheetFor(project, "", ""),
	}
}

// checkNewSession is the page's own check of a new session's name and
// description, in words; "" when both are fine. The root checks again.
func checkNewSession(name, description string) string {
	switch {
	case name == "":
		return "Give the session a name, like fix-login. It is also the name of its branch."
	case !sessionNamePattern.MatchString(name):
		return fmt.Sprintf("%q cannot be a session name: use lower-case letters, digits and dashes, starting with a letter or digit, like fix-login.", name)
	case strings.TrimSpace(description) == "":
		return "Say what the session should do. It plans its work from that, and with nothing to go on it would stop at once."
	case utf8.RuneCountInString(description) > maxDescriptionChars:
		return fmt.Sprintf("The description is %d characters long; the most is %d. Shorten it and try again.", utf8.RuneCountInString(description), maxDescriptionChars)
	}
	return ""
}

// checkLabel is the page's check of a label; "" when it is fine.
func checkLabel(label string) string {
	if n := utf8.RuneCountInString(label); n > maxLabelChars {
		return fmt.Sprintf("The label is %d characters long; the most is %d. Shorten it and try again.", n, maxLabelChars)
	}
	return ""
}

// SessionConfirmView is a no-JS confirm page of a session action. It has no
// JSON form: it asks the question the sheet asks, for a browser that cannot
// open a dialog.
type SessionConfirmView struct {
	Project string
	// Name is the session's name; for a new session, the name typed.
	Name  string
	Sheet sessionSheet
	// Offered is false when the action cannot be taken; Notice says why, or
	// what to fix in the new-session form.
	Offered bool
	Notice  string
	// Form is set when a new session's values must be fixed first.
	Form *newSessionForm
	// Back is where the safe link goes.
	Back string
}

// sessionConfirmPageFor is the pure decision behind a session's no-JS confirm
// page. ok is false for an action this page does not know.
func sessionConfirmPageFor(project string, s SessionView, action string, have actionSeams) (SessionConfirmView, bool) {
	o := offersFor(s)
	v := SessionConfirmView{Project: project, Name: s.Name, Back: sessionBase(project, s.Name)}
	switch action {
	case actionClean:
		if !have.Clean {
			return v, false
		}
		v.Sheet, v.Offered = cleanSheetFor(project, s), o.Clean
	case actionRestart:
		if !have.Restart {
			return v, false
		}
		v.Sheet, v.Offered, v.Notice = restartSheetFor(project, s), o.Restart, o.RestartWhy
	case actionLabel:
		if !have.Label {
			return v, false
		}
		v.Sheet, v.Offered, v.Notice = labelSheetFor(project, s), o.Label, o.LabelWhy
	default:
		return v, false
	}
	return v, true
}

// newConfirmPageFor is the pure decision behind the new session's no-JS
// page: with values that pass checkNewSession, the confirm; otherwise the
// form again, holding what was typed, with what to fix.
func newConfirmPageFor(project, name, description string) SessionConfirmView {
	name = strings.TrimSpace(name)
	v := SessionConfirmView{Project: project, Name: name, Back: sessionsBase(project)}
	if name == "" && description == "" {
		v.Sheet = sessionSheet{Title: "Start a new autonomous session"}
		v.Form = &newSessionForm{Page: sessionsBase(project) + "/new"}
		return v
	}
	if why := checkNewSession(name, description); why != "" {
		v.Sheet = sessionSheet{Title: "Start a new autonomous session"}
		v.Notice = why
		v.Form = &newSessionForm{Page: sessionsBase(project) + "/new", Name: name, Description: description}
		return v
	}
	v.Sheet, v.Offered = newSheetFor(project, name, strings.TrimSpace(description)), true
	return v
}

// SessionActionView is the API's answer to a session action: the project,
// the session and what happened, for every outcome.
type SessionActionView struct {
	Project string              `json:"project"`
	Session string              `json:"session"`
	Result  SessionActionResult `json:"result"`
}

// findSessionRow is the first row with that name, as the session screen
// picks it (see session in sessions.go).
func findSessionRow(rows []SessionView, name string) (SessionView, bool) {
	for _, sv := range rows {
		if sv.Name == name {
			return sv, true
		}
	}
	return SessionView{}, false
}

// noSuchSession is the 404 for a session the project does not have.
func (s *server) noSuchSession(w http.ResponseWriter, r *http.Request) {
	s.fail(w, r, http.StatusNotFound, "There is no session with that name in this project. It may have been cleaned up. Check the address, or go back to the project's sessions and pick it from the list.")
}

// sessionPost does the steps every action on an existing session shares, in
// the order setTaskStatus takes them: the project, the seam (none means the
// address is not found), the body, the session, and then the confirm.
// Nothing is called before the confirm is there. ok is false when the
// answer has been written.
func (s *server) sessionPost(w http.ResponseWriter, r *http.Request, haveSeam bool, needs string) (Project, SessionView, postBody, bool) {
	p, ok := s.findProject(w, r)
	if !ok {
		return p, SessionView{}, postBody{}, false
	}
	if !haveSeam {
		s.notFound(w, r)
		return p, SessionView{}, postBody{}, false
	}
	b, err := readPostBody(r)
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		s.tooLarge(w, r)
		return p, SessionView{}, postBody{}, false
	}
	sv, found := findSessionRow(s.d.Sessions(p.Root), r.PathValue("name"))
	if !found {
		s.noSuchSession(w, r)
		return p, sv, b, false
	}
	if err != nil || !b.confirmed() {
		s.fail(w, r, http.StatusBadRequest, "Nothing changed: "+needs+" needs the confirm. Open the session and use its Actions.")
		return p, sv, b, false
	}
	return p, sv, b, true
}

// bodyField reads one text field from a body already read: the form's
// value, or the JSON object's string. ok is false for JSON that is not an
// object or holds the field as something other than a string.
func bodyField(b postBody, name string) (string, bool) {
	if b.contentType != "application/json" {
		return b.form.Get(name), true
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b.json, &m); err != nil {
		return "", false
	}
	raw, ok := m[name]
	if !ok {
		return "", true
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", false
	}
	return v, true
}

// unreadable is the answer to a body whose fields could not be read.
func (s *server) unreadable(w http.ResponseWriter, r *http.Request, example string) {
	s.fail(w, r, http.StatusBadRequest, "Nothing changed: the request could not be read. Send it as JSON like "+example+".")
}

// answerAction writes a session action's answer. The page goes to done on
// success, so reloading the page it lands on posts nothing; a refusal or a
// failure is the error page with the seam's own sentence. The API answers
// the same body for every outcome, with the status saying which it was.
func (s *server) answerAction(w http.ResponseWriter, r *http.Request, project, name string, res SessionActionResult, done string) {
	status := http.StatusOK
	switch res.Outcome {
	case SessionActionDone:
	case SessionActionRefused:
		status = http.StatusBadRequest
	case SessionActionFailed:
		status = http.StatusInternalServerError
	default:
		status = http.StatusInternalServerError
		res = SessionActionResult{Outcome: SessionActionFailed, Text: "The action gave an answer this page does not know. This is a fault in afk; please report it. Open afk dash to see the session as it is now."}
	}
	if res.Text == "" && status != http.StatusOK {
		res.Text = "Nothing was done, and no reason was given. Open afk dash to see the session as it is now."
	}
	if isAPI(r.URL.Path) {
		writeJSON(w, status, SessionActionView{Project: project, Session: name, Result: res})
		return
	}
	if status != http.StatusOK {
		s.fail(w, r, status, res.Text)
		return
	}
	w.Header().Set("Location", done)
	w.WriteHeader(http.StatusSeeOther)
}

// refuse answers an action the page itself will not send to its seam.
func (s *server) refuse(w http.ResponseWriter, r *http.Request, project, name, text string) {
	s.answerAction(w, r, project, name, SessionActionResult{Outcome: SessionActionRefused, Text: text}, "")
}

// cleanSession is the clean POST: what `afk clean <name>` does, with the
// phase the page showed, through Deps.CleanSession.
func (s *server) cleanSession(w http.ResponseWriter, r *http.Request) {
	p, sv, b, ok := s.sessionPost(w, r, s.d.CleanSession != nil, "cleaning a session")
	if !ok {
		return
	}
	phase, ok := bodyField(b, "phase")
	if !ok {
		s.unreadable(w, r, `{"phase": "done", "confirm": true}`)
		return
	}
	res := s.d.CleanSession(p.Root, sv.Name, phase)
	s.answerAction(w, r, p.Name, sv.Name, res, sessionsBase(p.Name)+"?done=cleaned&session="+url.QueryEscape(sv.Name))
}

// restartSession is the restart POST. A row that offersFor would not offer
// a restart is refused here without calling the seam.
func (s *server) restartSession(w http.ResponseWriter, r *http.Request) {
	p, sv, _, ok := s.sessionPost(w, r, s.d.RestartSession != nil, "restarting a session")
	if !ok {
		return
	}
	if o := offersFor(sv); !o.Restart {
		s.refuse(w, r, p.Name, sv.Name, "Nothing changed: "+o.RestartWhy)
		return
	}
	res := s.d.RestartSession(p.Root, sv.Name)
	s.answerAction(w, r, p.Name, sv.Name, res, sessionBase(p.Name, sv.Name)+"?done=restarted")
}

// labelSession is the label POST. A blank label clears it.
func (s *server) labelSession(w http.ResponseWriter, r *http.Request) {
	p, sv, b, ok := s.sessionPost(w, r, s.d.SetSessionLabel != nil, "setting a label")
	if !ok {
		return
	}
	if o := offersFor(sv); !o.Label {
		s.refuse(w, r, p.Name, sv.Name, "Nothing changed: "+o.LabelWhy)
		return
	}
	label, ok := bodyField(b, "label")
	if !ok {
		s.unreadable(w, r, `{"label": "login fix", "confirm": true}`)
		return
	}
	label = strings.TrimSpace(label)
	if why := checkLabel(label); why != "" {
		s.refuse(w, r, p.Name, sv.Name, "Nothing changed: "+why)
		return
	}
	res := s.d.SetSessionLabel(p.Root, sv.Name, label)
	s.answerAction(w, r, p.Name, sv.Name, res, sessionBase(p.Name, sv.Name)+"?done=labelled")
}

// newSession is the POST on the list: what `afk new --autonomous
// --description D <name>` does, through Deps.NewSession. Nothing else about
// the session comes from the request: not its folder, not its base, not how
// it runs.
func (s *server) newSession(w http.ResponseWriter, r *http.Request) {
	p, ok := s.findProject(w, r)
	if !ok {
		return
	}
	if s.d.NewSession == nil {
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
		s.fail(w, r, http.StatusBadRequest, "Nothing changed: starting a session needs the confirm. Open the project's sessions and press Start session.")
		return
	}
	name, okName := bodyField(b, "name")
	description, okDesc := bodyField(b, "description")
	if !okName || !okDesc {
		s.unreadable(w, r, `{"name": "fix-login", "description": "Fix the login form", "confirm": true}`)
		return
	}
	name, description = strings.TrimSpace(name), strings.TrimSpace(description)
	if why := checkNewSession(name, description); why != "" {
		s.refuse(w, r, p.Name, name, "Nothing started: "+why)
		return
	}
	res := s.d.NewSession(p.Root, name, description)
	s.answerAction(w, r, p.Name, name, res, sessionBase(p.Name, name)+"?done=started")
}

// sessionConfirmPage is the no-JS page behind a session's Actions rows. It
// is not in routes: it has no JSON form, so it must not get an /api twin.
// An action without its seam, or one this page does not know, is the
// catch-all 404, as its POST is.
func (s *server) sessionConfirmPage(w http.ResponseWriter, r *http.Request) {
	p, ok := s.findProject(w, r)
	if !ok {
		return
	}
	sv, found := findSessionRow(s.d.Sessions(p.Root), r.PathValue("name"))
	v, known := sessionConfirmPageFor(p.Name, sv, r.PathValue("action"), s.seams())
	if !known {
		s.notFound(w, r)
		return
	}
	if !found {
		s.noSuchSession(w, r)
		return
	}
	s.renderPage(w, r, http.StatusOK, "session_confirm", v)
}

// sessionNewPage is the no-JS page behind the list's Start session: the
// values typed and the confirm, or the form again with what to fix.
//
// Its address is /sessions/new, which the mux prefers over /sessions/{name}
// as the more specific pattern, so a session that is itself named "new"
// cannot be opened by its address on the page; the list still shows it,
// and the API (which has no such page) reaches it.
func (s *server) sessionNewPage(w http.ResponseWriter, r *http.Request) {
	p, ok := s.findProject(w, r)
	if !ok {
		return
	}
	if s.d.NewSession == nil {
		s.notFound(w, r)
		return
	}
	q := r.URL.Query()
	s.renderPage(w, r, http.StatusOK, "session_confirm", newConfirmPageFor(p.Name, q.Get("name"), q.Get("description")))
}

// sessionToastCodes are the toast codes the session actions lead to. They
// belong to the Sessions screens only (sessionsToast, sessionToast):
// toastFor leaves them out, so another screen's address cannot show them.
var sessionToastCodes = map[string]bool{"cleaned": true, "restarted": true, "labelled": true, "started": true}

// sessionsToast is the toast on the list after a clean. The session named by
// the query is shown only when it is a plain session name and no longer on
// the list: a name still listed was not cleaned (or another session of that
// name remains), and an odd one is not echoed at all.
func sessionsToast(v SessionsView, code, name string) string {
	if code != "cleaned" {
		return ""
	}
	if !sessionNamePattern.MatchString(name) || len(name) > 64 {
		return "Session cleaned."
	}
	for _, row := range v.Sessions {
		if row.Name == name {
			return "Session cleaned."
		}
	}
	return strings.ReplaceAll(toastTexts["cleaned"], "{session}", name)
}

// sessionToast is the toast on a session's screen after a restart, a label
// or a start. The name is the server's own read of the session, never the
// query's.
func sessionToast(v SessionScreenView, code string) string {
	if code == "cleaned" || !sessionToastCodes[code] {
		return ""
	}
	return strings.ReplaceAll(toastTexts[code], "{session}", v.Name)
}
