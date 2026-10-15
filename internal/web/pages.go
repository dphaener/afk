package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dphaener/afk/internal/inbox"
)

// pageFiles holds the page templates inside the binary, so the page needs no
// files next to it and no build step.
//
//go:embed templates/*.html
var pageFiles embed.FS

// pageNames are the views that have a page. Each is layout.html plus the file
// of the same name.
var pageNames = []string{"overview", "usage", "project", "plan", "inbox", "item", "tasks", "task", "task_status", "sessions", "session", "session_confirm", "confirm", "plan_confirm", "plan_result", "plan_chat", "plan_change", "plan_new", "error"}

// parsePages parses every page once. One set per page, because every page
// defines the same block names ("title", "content") and a single shared set
// would let the last one parsed win.
//
// Nothing here marks a value as already safe (html/template has types for
// raw HTML, scripts and styles): everything from a project's files, an inbox
// item included, goes through its escaping. There is one exception in the
// package and it is not here: the rendered master plan on the plan view
// (PlanView.Rendered) is template.HTML, because a plan drawn as headings,
// lists and tables has to carry tags. renderPlan in markdown.go builds it
// and escapes every piece of the plan's text itself before joining it to a
// tag; its doc comment has the argument. The planning chat's agent messages
// go through the same renderer in its chat mode (renderChat, the "renderChat"
// function below): the same code and the same escaping, used in a second
// place, so it is this exception and not another. The owner's own messages
// are not rendered at all; they are plain text the template escapes. Do not
// add a second exception.
func parsePages(asset func(name string) string) (map[string]*template.Template, error) {
	funcs := template.FuncMap{
		"when":  whenUTC,
		"asset": asset,
		"pe":    url.PathEscape,
		"pct":   func(f float64) string { return fmt.Sprintf("%.0f%%", f) },
		"sheet": confirmSheetFor,
		// planApprovePath is the Approve the plan… link's address, the no-JS
		// confirm page, which the dialog's script replaces when it runs.
		"planApprovePath": planApprovePath,
		// renderChat draws one agent message of the planning chat (see the
		// exception above).
		"renderChat": renderChat,
	}
	out := make(map[string]*template.Template, len(pageNames))
	for _, name := range pageNames {
		t, err := template.New("layout.html").Funcs(funcs).ParseFS(pageFiles, "templates/layout.html", "templates/parts.html", "templates/"+name+".html")
		if err != nil {
			return nil, fmt.Errorf("the web page templates are damaged (%s): %w", name, err)
		}
		out[name] = t
	}
	return out, nil
}

// whenUTC shows a time in UTC in a short form. It takes the three shapes the
// views hold times in: a time, a pointer to one (nil is blank) and an RFC
// 3339 string as stored in the ledger and the inbox. A string that does not
// parse is shown as it is, so nothing is hidden.
func whenUTC(v any) string {
	const layout = "2 Jan 2006 15:04 UTC"
	switch t := v.(type) {
	case time.Time:
		if t.IsZero() {
			return ""
		}
		return t.UTC().Format(layout)
	case *time.Time:
		if t == nil || t.IsZero() {
			return ""
		}
		return t.UTC().Format(layout)
	case string:
		if t == "" {
			return ""
		}
		if parsed, err := time.Parse(time.RFC3339, t); err == nil {
			return parsed.UTC().Format(layout)
		}
		return t
	}
	return ""
}

// pageData is what every page template set is executed with. The layout
// reads Chrome for the bars and hands View to the page's own blocks
// ({{template "content" .View}}), so a page template sees exactly the view
// respond was given, the same value the JSON API encodes. Wrapping here,
// rather than adding bar fields to each view type, is what keeps the API's
// answers unchanged by the page's looks.
type pageData struct {
	View   any
	Chrome chrome
}

// chrome is what the layout's bars need to know about the page they sit on.
// None of it is read from a project's files except Projects, which counts the
// daemon's project list on each request like every other read here.
type chrome struct {
	// Bare drops the bars and the live script: the error page stands alone,
	// as the design's Error screen does, because the request that failed may
	// be the very one that would have drawn the bars' data.
	Bare bool
	// Overview is true on the home page, where the phone bar shows the
	// wordmark instead of a back button and a title.
	Overview bool
	// Title is the phone bar's title; Back and BackLabel are its back
	// button's address and its spoken name (the button is an icon only).
	Title, Back, BackLabel string
	// Nav is the top bar's current item, "projects" or "usage", and Current
	// is the aria-current value to give it: "page" when the page is that
	// item's own address, "true" when it is a page under it (a project, a
	// plan, an item), because aria-current="page" on a link to / would tell
	// a screen reader the owner is on / when they are not.
	Nav, Current string
	// Projects is the number beside Projects in the top bar.
	Projects int
	// Status, Path and Retry are for the error page: the status and the
	// address that failed, shown in mono, and where Try again goes.
	Status      int
	Path, Retry string
	// Toast is the sentence the layout shows after a pause or resume, or ""
	// for none. It is only ever one of toastTexts (see toastFor).
	Toast string
	// PlanNew is whether the page offers Plan a new project (canPlanNew):
	// the top bar's action and the overview's footer button are drawn only
	// then, so neither can lead to an address that answers 404. It is set by
	// renderPage, not chromeFor, because it is the server's seams and not
	// the page's view that decide it.
	PlanNew bool
}

// chromeFor works out the bars for a page from its name and view, the
// request's method and path and the number of projects. It is pure so every
// page's bar is a table row in the tests.
func chromeFor(name string, view any, method, path string, projects int) chrome {
	c := chrome{Nav: "projects", Current: "true", Projects: projects, Back: "/", BackLabel: "Back to all projects"}
	switch v := view.(type) {
	case OverviewView:
		c.Overview, c.Title, c.Current = true, "All projects", "page"
	case UsageView:
		c.Title, c.Nav, c.Current = "Usage", "usage", "page"
	case ProjectView:
		c.Title = v.Name
	case PlanView:
		c.Title = "Plan: " + v.Project
		// A plan waiting for approval has no project page to go back to:
		// findProject answers 404 for it.
		if !v.WaitingForApproval {
			c.Back, c.BackLabel = "/p/"+url.PathEscape(v.Project), "Back to "+v.Project
		}
	case InboxView:
		c.Title = "Inbox"
		c.Back, c.BackLabel = "/p/"+url.PathEscape(v.Project), "Back to "+v.Project
	case ItemView:
		c.Title = v.Title
		c.Back, c.BackLabel = "/p/"+url.PathEscape(v.Project)+"/inbox", "Back to the inbox of "+v.Project
	case TasksView:
		c.Title = "Tasks"
		c.Back, c.BackLabel = "/p/"+url.PathEscape(v.Project), "Back to "+v.Project
	case TaskView:
		c.Title = v.ID
		c.Back, c.BackLabel = tasksBase(v.Project), "Back to the tasks of "+v.Project
	case SessionsView:
		c.Title = "Sessions"
		c.Back, c.BackLabel = "/p/"+url.PathEscape(v.Project), "Back to "+v.Project
	case SessionScreenView:
		c.Title = v.Name
		c.Back, c.BackLabel = sessionsBase(v.Project), "Back to the sessions of "+v.Project
	case TaskStatusPageView:
		c.Title = "Set status"
		c.Back, c.BackLabel = taskBase(v.Project, v.ID), "Back to "+v.ID
	case SessionConfirmView:
		// The bar's title is the action in a word; the question itself is
		// the page's heading, too long for the phone bar.
		c.Title, c.Back, c.BackLabel = sessionActionWords[v.Sheet.Action], v.Back, "Back to "+v.Name
		if v.Sheet.Action == actionNew || v.Sheet.Action == "" {
			c.Title, c.BackLabel = sessionActionWords[actionNew], "Back to the sessions of "+v.Project
		}
	case PlanConfirmView:
		// Back to the plan, not the project: a plan on the review list has no
		// project page.
		c.Title = "Approve the plan"
		c.Back, c.BackLabel = v.Back, "Back to the plan of "+v.Project
	case ChatView:
		c.Title = "Planning " + v.Project
		c.Back, c.BackLabel = "/p/"+url.PathEscape(v.Project), "Back to "+v.Project
	case ChangePageView:
		c.Title = "Change the plan"
		c.Back, c.BackLabel = v.Back, "Back to "+v.Project
	case NewPlanPageView:
		c.Title = "Plan a new project"
	case PlanResultView:
		c.Title = v.Title
		c.Back, c.BackLabel = v.Back, "Back to the plan of "+v.Project
	case ConfirmView:
		c.Title = v.Sheet.Action
		c.Back, c.BackLabel = "/p/"+url.PathEscape(v.Project), "Back to "+v.Project
	case ErrorView:
		return chrome{Bare: true, Status: v.Status, Path: path, Retry: retryPath(method, path)}
	default:
		c.Title = name
	}
	return c
}

// retryPath is where the error page's Try again goes. A failed GET is asked
// for again. A failed POST is not, because a link cannot post and a GET of a
// POST address is a 404: it goes to the page the form was on, which for every
// POST route (an answer, a pause, a resume) is the address with its last
// part taken off. The leading slashes are collapsed as in withoutToken, so an
// address that reached here oddly can never become a link to another site.
func retryPath(method, path string) string {
	p := "/" + strings.TrimLeft(path, `/\`)
	if method != http.MethodGet && method != http.MethodHead {
		if i := strings.LastIndex(p, "/"); i > 0 {
			p = p[:i]
		} else {
			p = "/"
		}
	}
	return (&url.URL{Path: p}).EscapedPath()
}

// renderPage writes the page for a view. The page is built in memory first
// so a template fault becomes a clean 500 and not half a page after a 200.
func (s *server) renderPage(w http.ResponseWriter, r *http.Request, status int, name string, view any) {
	t, ok := s.pages[name]
	var buf bytes.Buffer
	if !ok {
		status = http.StatusInternalServerError
		buf.WriteString("<!doctype html><title>afk</title><p>This page is missing. This is a fault in afk; please report it.</p>")
	} else {
		projects := 0
		if _, isErr := view.(ErrorView); !isErr {
			projects = len(s.projects())
		}
		ch := chromeFor(name, view, r.Method, r.URL.Path, projects)
		ch.PlanNew = !ch.Bare && s.canPlanNew()
		if pv, isProject := view.(ProjectView); isProject && r.Method == http.MethodGet {
			ch.Toast = toastFor(r.URL.Query().Get("done"), pv.Name, "")
		}
		if iv, isInbox := view.(InboxView); isInbox && r.Method == http.MethodGet {
			ch.Toast = inboxToast(iv, r.URL.Query().Get("done"), r.URL.Query().Get("item"))
		}
		if tv, isTask := view.(TaskView); isTask && r.Method == http.MethodGet {
			ch.Toast = taskToast(tv, r.URL.Query().Get("done"))
		}
		if sv, isSessions := view.(SessionsView); isSessions && r.Method == http.MethodGet {
			ch.Toast = sessionsToast(sv, r.URL.Query().Get("done"), r.URL.Query().Get("session"))
		}
		if sv, isSession := view.(SessionScreenView); isSession && r.Method == http.MethodGet {
			ch.Toast = sessionToast(sv, r.URL.Query().Get("done"))
		}
		if _, isChat := view.(ChatView); isChat && r.Method == http.MethodGet {
			ch.Toast = chatToast(r.URL.Query().Get("done"))
		}
		if _, isUsage := view.(UsageView); isUsage && r.Method == http.MethodGet {
			ch.Toast = usageToast(r.URL.Query().Get("done"))
		}
		data := pageData{View: view, Chrome: ch}
		if err := t.Execute(&buf, data); err != nil {
			status = http.StatusInternalServerError
			buf.Reset()
			buf.WriteString("<!doctype html><meta name=\"viewport\" content=\"width=device-width, initial-scale=1\"><title>afk</title><p>The page could not be drawn. This is a fault in afk; please report it.</p>")
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// redirectAfter is where a successful page POST goes next, or "" when the
// answer is not one: an answer goes to the inbox list, a pause or resume to the
// project. An answer goes back to the inbox list, with a code for the toast
// and the item it was for.
func redirectAfter(r *http.Request, status int, name string, view any) string {
	project := "/p/" + url.PathEscape(r.PathValue("project"))
	switch {
	case r.Method == http.MethodPost && status == http.StatusOK && name == "item":
		if iv, ok := view.(ItemView); ok {
			// The code was chosen by the handler from the stored item and
			// the ledger, never from the request, so the toast it leads to
			// echoes nothing the request carried.
			code := iv.DoneCode
			if code == "" {
				code = "answered"
			}
			return project + "/inbox?done=" + url.QueryEscape(code) + "&item=" + url.QueryEscape(iv.ID)
		}
	case r.Method == http.MethodPost && status == http.StatusOK && name == "project":
		// The code is fixed text chosen from the address, never anything the
		// request carried, so the toast it leads to echoes nothing.
		switch {
		case strings.HasSuffix(r.URL.Path, "/pause"):
			return project + "?done=paused"
		case strings.HasSuffix(r.URL.Path, "/resume"):
			return project + "?done=resumed"
		}
		return project
	}
	return ""
}

// toastTexts maps the codes redirectAfter appends to the sentence the toast
// shows. {project} is replaced with the project's name from the server's own
// list. The layout draws a toast only for a key here, so a hostile or stale
// ?done= value shows nothing and is never printed.
var toastTexts = map[string]string{
	"paused":  "Paused {project}. Nothing new will start.",
	"resumed": "Resumed {project}.",
	// The three codes an answer leads to. {task} is the task of the item the
	// answer was for, read by the server (see inboxToast).
	"answered": "Answer sent.",
	"ready":    "Answer sent. {task} is ready again.",
	"done":     "Marked as done.",
	// The two codes a task's status change leads to. {task} and {status} are
	// the task's id and its status as the server reads them from the ledger
	// after the change (see taskToast), never from the request.
	"status":  "{task} is now {status}.",
	"already": "{task} was already {status}. Nothing changed.",
	// The two codes Refresh usage leads to, shown only on the usage screen
	// (see usageToast).
	"refreshing":         "Reading usage. This can take up to a minute.",
	"already_refreshing": "A usage reading is already running.",
	// The four codes the session actions lead to, shown only on the
	// Sessions screens (see sessionsToast and sessionToast). {session} is
	// the session's name as the server reads it.
	"cleaned":   "Cleaned {session}.",
	"restarted": "Restarted {session}.",
	"labelled":  "Label saved.",
	"started":   "Started {session}. It works on its own; open its terminal from afk dash or afk attach.",
	// The code a reply from the planning chat leads to, shown only on the
	// chat screen (see chatToast).
	"sent": "Sent to the planning agent.",
}

// toastFor is the toast sentence for a ?done= code, or "" when the code is
// not in toastTexts. The name comes from the project list and the task from
// the server's own read of an item, never from the query; html/template
// escapes both when drawn. A text that names a task gets no toast when there
// is no task to name, so a placeholder is never shown.
func toastFor(code, project, task string) string {
	text, ok := toastTexts[code]
	if !ok || refreshToastCodes[code] || sessionToastCodes[code] || chatToastCodes[code] {
		// A refresh code belongs to the usage screen (usageToast); on a
		// project or the inbox it would say a reading started when none did.
		// A session code belongs to the Sessions screens, and a chat code to
		// the chat screen, for the same reason.
		return ""
	}
	if strings.Contains(text, "{task}") && task == "" {
		return ""
	}
	// A text that names a status belongs on a task's screen only (taskToast),
	// where the status is read from the ledger; anywhere else there is no
	// status to name, and a placeholder must never show.
	if strings.Contains(text, "{status}") {
		return ""
	}
	text = strings.ReplaceAll(text, "{project}", project)
	return strings.ReplaceAll(text, "{task}", task)
}

// inboxToast is the toast on the inbox list after an answer. The item named
// by the query must be one the server's own read of the inbox holds: an
// unknown or malformed id shows nothing, so the address cannot make the page
// say anything about a task that has no item.
func inboxToast(v InboxView, code, id string) string {
	if !inbox.ValidID(id) {
		return ""
	}
	for _, it := range v.Items {
		if it.ID == id {
			return toastFor(code, v.Project, it.Task)
		}
	}
	return ""
}

// ConfirmSheet is what the shared confirm-sheet partial draws and what the
// no-JS confirm page repeats, so the two always say the same thing.
type ConfirmSheet struct {
	// ID is the dialog's id, which a link's data-confirm names.
	ID string
	// Title is the question naming the thing; Body is one sentence on what
	// happens and what is not touched.
	Title, Body string
	// Action is the action button's label and Loading its "-ing" form shown
	// while the form is sent.
	Action, Loading string
	Danger          bool
	// Method and Path are the form's; Safe is the harmless button's label.
	Method, Path, Safe string
}

// ConfirmView is the no-JS confirm page. It has no JSON form: the page is
// the same question the sheet asks, for a browser that cannot open a dialog.
type ConfirmView struct {
	Project string
	Sheet   ConfirmSheet
}

// confirmSheetFor builds the sheet for pausing a project, or for resuming it
// when resume is true. It is the one place the wording lives.
func confirmSheetFor(project string, resume bool) ConfirmSheet {
	base := "/p/" + url.PathEscape(project)
	if resume {
		return ConfirmSheet{
			ID: "confirm-resume", Title: "Resume " + project + "?",
			Body:   "afk starts the next ready task again; a stopped line is cleared. Nothing is deleted.",
			Action: "Resume project", Loading: "Resuming",
			Method: "post", Path: base + "/resume", Safe: "Keep it paused",
		}
	}
	return ConfirmSheet{
		ID: "confirm-pause", Title: "Pause " + project + "?",
		Body:   "The running task finishes; nothing new starts. Nothing is deleted.",
		Action: "Pause project", Loading: "Pausing",
		Method: "post", Path: base + "/pause", Safe: "Keep it running",
	}
}
