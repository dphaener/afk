package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"time"
)

// pageFiles holds the page templates inside the binary, so the page needs no
// files next to it and no build step.
//
//go:embed templates/*.html
var pageFiles embed.FS

// pageNames are the views that have a page. Each is layout.html plus the file
// of the same name.
var pageNames = []string{"overview", "usage", "project", "plan", "item", "error"}

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
// tag; its doc comment has the argument. Do not add a second exception.
func parsePages() (map[string]*template.Template, error) {
	funcs := template.FuncMap{
		"when": whenUTC,
		"pe":   url.PathEscape,
		"pct":  func(f float64) string { return fmt.Sprintf("%.0f%%", f) },
	}
	out := make(map[string]*template.Template, len(pageNames))
	for _, name := range pageNames {
		t, err := template.New("layout.html").Funcs(funcs).ParseFS(pageFiles, "templates/layout.html", "templates/"+name+".html")
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

// renderPage writes the page for a view. The page is built in memory first
// so a template fault becomes a clean 500 and not half a page after a 200.
func (s *server) renderPage(w http.ResponseWriter, status int, name string, view any) {
	t, ok := s.pages[name]
	var buf bytes.Buffer
	if !ok {
		status = http.StatusInternalServerError
		buf.WriteString("<!doctype html><title>afk</title><p>This page is missing. This is a fault in afk; please report it.</p>")
	} else if err := t.Execute(&buf, view); err != nil {
		status = http.StatusInternalServerError
		buf.Reset()
		buf.WriteString("<!doctype html><meta name=\"viewport\" content=\"width=device-width, initial-scale=1\"><title>afk</title><p>The page could not be drawn. This is a fault in afk; please report it.</p>")
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// redirectAfter is where a successful page POST goes next, or "" when the
// answer is not one: an answer goes to its item, a pause or resume to the
// project. The inbox list page has no template of its own and goes to the
// inbox section of the project page.
func redirectAfter(r *http.Request, status int, name string, view any) string {
	project := "/p/" + url.PathEscape(r.PathValue("project"))
	switch {
	case r.Method == http.MethodPost && status == http.StatusOK && name == "item":
		if iv, ok := view.(ItemView); ok {
			return project + "/inbox/" + url.PathEscape(iv.ID)
		}
	case r.Method == http.MethodPost && status == http.StatusOK && name == "project":
		return project
	case r.Method == http.MethodGet && status == http.StatusOK && name == "inbox":
		return project + "#inbox"
	}
	return ""
}
