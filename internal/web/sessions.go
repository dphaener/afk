package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// This file is the Sessions screens (docs/design-system.md §3 "Sessions list
// and session"): every session of a project, and one session's overview,
// the facts the dashboard's overview panel shows, in words. Both only read.
//
// The page cannot open a session's terminal: a terminal needs the session's
// host, and this package never dials one (the handlers read files, and
// Deps.Sessions must not block). The session screen says where the terminal
// is opened instead. Actions on a session (restart, label, clean) are not
// here either; they come with their own confirm sheets in a later task.
//
// The facts come from Deps.Sessions, discovery's rows copied into
// SessionView. internal/ui's overview panel draws the same facts with Lip
// Gloss for a terminal, which this package cannot import; the builders here
// are the page's own, kept pure so each case is a table row.

// The kinds of row the list holds, as sessionKind decides them and the JSON
// names them.
const (
	sessionKindProblem  = "problem"
	sessionKindPlanning = "planning"
	sessionKindOneOff   = "one_off"
	sessionKindUnknown  = "status_unknown"
	sessionKindSession  = "session"
)

// shortIDLen is how much of a session id the list shows. Eight characters
// tell one session from another on a project's list; the session's own
// screen shows the whole id.
const shortIDLen = 8

// SessionFacts is one session as the API answers it, in the list and on the
// session's own screen. Every key is always there: lists are [] and never
// null, and a time is RFC 3339 in UTC or "" when it is not known.
type SessionFacts struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	// Kind is what the row is: "session", "one_off", "planning",
	// "status_unknown" (status.json could not be read) or "problem" (a
	// worktree problem, not a session).
	Kind  string `json:"kind"`
	Phase string `json:"phase"`
	// WaitsForYou is AwaitingInput as the dashboard shows it: never for a
	// session that is done or failed, whose flag can be left over.
	WaitsForYou   bool   `json:"waits_for_you"`
	AwaitingInput bool   `json:"awaiting_input"`
	LoopCount     int    `json:"loop_count"`
	HostRunning   bool   `json:"host_running"`
	Wedged        bool   `json:"wedged"`
	OneOff        bool   `json:"one_off"`
	Planning      bool   `json:"planning"`
	Autonomous    bool   `json:"autonomous"`
	StatusStale   bool   `json:"status_stale"`
	StaleReason   string `json:"stale_reason"`
	Problem       string `json:"problem"`
	SessionID     string `json:"session_id"`
	Branch        string `json:"branch"`
	BaseBranch    string `json:"base_branch"`
	WorktreePath  string `json:"worktree_path"`
	StartedAt     string `json:"started_at"`
	UpdatedAt     string `json:"updated_at"`
	PlanTotal     int    `json:"plan_total"`
	PlanDone      int    `json:"plan_done"`
	PlanFailed    int    `json:"plan_failed"`
	PlanError     string `json:"plan_error"`
	// Verdict is "APPROVE", "REJECT" or "" when not reviewed yet.
	Verdict       string         `json:"verdict"`
	VerdictChecks []SessionCheck `json:"verdict_checks"`
	VerdictError  string         `json:"verdict_error"`
}

// SessionsView is a project's sessions list, in discovery's order.
type SessionsView struct {
	Project  string       `json:"project"`
	Sessions []SessionRow `json:"sessions"`
	// Page is the list laid out for the page; the JSON leaves it out.
	Page sessionsPage `json:"-"`
}

// sessionsPage is what sessions.html draws besides the rows.
type sessionsPage struct {
	// Empty is the empty state's sentence, or "" when there are sessions.
	Empty string
}

// SessionRow is one row of the list: the session's facts, and how the page
// draws it.
type SessionRow struct {
	SessionFacts
	// Href is the session's screen; Title the label, or the name.
	Href  string `json:"-"`
	Title string `json:"-"`
	// Badges are drawn in order; Lines are the sentences under the row
	// saying what an unusual row is.
	Badges []badgeView `json:"-"`
	Lines  []string    `json:"-"`
	// ShortID is the first characters of the session id, "" when it has none.
	ShortID string `json:"-"`
	// Done and Total feed the shared progress partial; Total is zero when
	// the session has no work plan, and then no bar is drawn.
	Done  int `json:"-"`
	Total int `json:"-"`
}

// SessionScreenView is one session's screen.
type SessionScreenView struct {
	Project string `json:"project"`
	SessionFacts
	// Page is the session laid out for the page; the JSON leaves it out.
	Page sessionPage `json:"-"`
}

// sessionPage is what session.html draws besides the facts.
type sessionPage struct {
	Title  string
	Badges []badgeView
	// Lines say what an unusual row is, as on the list.
	Lines []string
	KV    []kvRow
	// Summary is the overview in sentences, one to a paragraph.
	Summary []string
}

// kvRow is one label / value pair of the session's kv list. Code draws the
// value as code (the folder), selectable and wrapping anywhere.
type kvRow struct {
	Label, Value string
	Code         bool
}

// kindInfo is what sessionKind decides: the row's kind, its badge and the
// line that says what it is ("" for an ordinary session).
type kindInfo struct {
	Kind  string
	Badge badgeView
	Line  string
}

// sessionKind says what a row is. The order matters and each step is a test
// row: a worktree problem is not a session at all, so nothing else about it
// counts; the planning session is named before the one-off it usually also
// is (its host runs with no status.json), because "planning" says more; a
// one-off is never "status unknown", because having no status.json is its
// normal shape; and only then does a status.json that could not be read
// leave the phase unknown. Everything else shows its phase.
func sessionKind(s SessionView) kindInfo {
	switch {
	case s.Problem != "":
		return kindInfo{Kind: sessionKindProblem, Badge: badgeView{Class: "badge--failed", Word: "Problem"}, Line: s.Problem}
	case s.Planning:
		return kindInfo{Kind: sessionKindPlanning, Badge: badgeView{Class: "badge--plain", Word: "Planning"}, Line: "The planning session for this project's master plan"}
	case s.Scratch:
		return kindInfo{Kind: sessionKindOneOff, Badge: badgeView{Class: "badge--plain", Word: "One-off"}, Line: "A one-off session with no plan: only a terminal"}
	case s.StatusStale:
		return kindInfo{Kind: sessionKindUnknown, Badge: badgeView{Class: "badge--warning", Word: "Status unknown"}, Line: "status.json could not be read: " + reasonText(s.StaleReason)}
	}
	return kindInfo{Kind: sessionKindSession, Badge: phaseBadge(s.Phase)}
}

// reasonText is a stale reason for a sentence, with a word in place of none.
func reasonText(r string) string {
	if r == "" {
		return "no reason was given"
	}
	return r
}

// phaseBadge is a session phase's badge: the four working phases in the
// running colour, done and failed in theirs, and a word this binary does not
// know as written on the plain badge.
func phaseBadge(phase string) badgeView {
	switch phase {
	case "planning", "executing", "reviewing", "fixing":
		return badgeView{Class: "badge--running", Word: phase}
	case "done":
		return badgeView{Class: "badge--done", Word: phase}
	case "failed":
		return badgeView{Class: "badge--failed", Word: phase}
	case "":
		return badgeView{Class: "badge--plain", Word: "unknown"}
	}
	return badgeView{Class: "badge--plain", Word: phase}
}

// waitsForYou is the dashboard's showsAwaitingInput rule: a done or failed
// session's awaiting_input can be left over from before it ended, and it
// cannot be answered any more, so it is not shown.
func waitsForYou(s SessionView) bool {
	return s.AwaitingInput && s.Phase != "done" && s.Phase != "failed"
}

// hostStopped reports whether the row should say its host is not running.
// A worktree problem row has no session and so no host to speak of.
func hostStopped(s SessionView) bool {
	return s.Problem == "" && !s.HostRunning
}

// quiet reports whether the row should say its agent has gone quiet. Only a
// running host's mark counts: a stopped host's host.json can still carry the
// mark it had when it stopped, and "not running" already says more.
func quiet(s SessionView) bool {
	return s.Problem == "" && s.HostRunning && s.Wedged
}

// sessionBadges are a row's badges in drawing order: what it is (or its
// phase), Waits for you, Stopped, then the review verdict.
func sessionBadges(s SessionView, k kindInfo) []badgeView {
	out := []badgeView{k.Badge}
	if waitsForYou(s) {
		out = append(out, badgeView{Class: "badge--needs-you", Word: "Waits for you"})
	}
	if hostStopped(s) {
		out = append(out, badgeView{Class: "badge--idle", Word: "Stopped"})
	}
	if v, ok := verdictBadge(s.Verdict); ok {
		out = append(out, v)
	}
	return out
}

// verdictBadge is the review verdict's badge, and false when the session has
// not been reviewed (or holds a verdict word this binary does not know).
func verdictBadge(verdict string) (badgeView, bool) {
	switch verdict {
	case "APPROVE":
		return badgeView{Class: "badge--pass", Word: "Approved"}, true
	case "REJECT":
		return badgeView{Class: "badge--failed", Word: "Changes asked"}, true
	}
	return badgeView{}, false
}

// sessionLines are the sentences under a row: what an unusual row is, then
// the host's state when it is not plainly running.
func sessionLines(s SessionView, k kindInfo) []string {
	var out []string
	if k.Line != "" {
		out = append(out, k.Line)
	}
	if hostStopped(s) {
		out = append(out, "Its host is not running")
	}
	if quiet(s) {
		out = append(out, "Its agent has gone quiet")
	}
	return out
}

// rfc3339 is a time for the JSON: RFC 3339 in UTC, or "" for the zero time.
func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// factsOf copies a session's facts for the JSON.
func factsOf(s SessionView, k kindInfo) SessionFacts {
	f := SessionFacts{
		Name: s.Name, Label: s.Label, Kind: k.Kind, Phase: s.Phase,
		WaitsForYou: waitsForYou(s), AwaitingInput: s.AwaitingInput, LoopCount: s.LoopCount,
		HostRunning: s.HostRunning, Wedged: s.Wedged, OneOff: s.Scratch, Planning: s.Planning,
		Autonomous: s.Autonomous, StatusStale: s.StatusStale, StaleReason: s.StaleReason, Problem: s.Problem,
		SessionID: s.SessionID, Branch: s.Branch, BaseBranch: s.BaseBranch, WorktreePath: s.WorktreePath,
		StartedAt: rfc3339(s.StartedAt), UpdatedAt: rfc3339(s.UpdatedAt),
		PlanTotal: s.PlanTotal, PlanDone: s.PlanDone, PlanFailed: s.PlanFailed, PlanError: s.PlanError,
		Verdict: s.Verdict, VerdictChecks: []SessionCheck{}, VerdictError: s.VerdictError,
	}
	f.VerdictChecks = append(f.VerdictChecks, s.VerdictChecks...)
	return f
}

// titleOf is what a session is called on the page: its label when it has
// one, else its name.
func titleOf(s SessionView) string {
	if s.Label != "" {
		return s.Label
	}
	return s.Name
}

// shortID is the first shortIDLen characters of a session id.
func shortID(id string) string {
	r := []rune(id)
	if len(r) <= shortIDLen {
		return id
	}
	return string(r[:shortIDLen])
}

// sessionsBase is the address of a project's sessions list.
func sessionsBase(project string) string {
	return "/p/" + url.PathEscape(project) + "/sessions"
}

// sessionBase is the address of one session's screen.
func sessionBase(project, name string) string {
	return sessionsBase(project) + "/" + url.PathEscape(name)
}

// sessionsPageFor lays out a project's sessions list, in the order the rows
// come in (discovery's). Pure.
func sessionsPageFor(project string, sessions []SessionView) SessionsView {
	v := SessionsView{Project: project, Sessions: []SessionRow{}}
	for _, s := range sessions {
		k := sessionKind(s)
		row := SessionRow{
			SessionFacts: factsOf(s, k),
			Href:         sessionBase(project, s.Name), Title: titleOf(s),
			Badges: sessionBadges(s, k), Lines: sessionLines(s, k),
			ShortID: shortID(s.SessionID), Done: s.PlanDone, Total: s.PlanTotal,
		}
		v.Sessions = append(v.Sessions, row)
	}
	if len(v.Sessions) == 0 {
		v.Page.Empty = "No sessions yet"
	}
	return v
}

// sessionPageFor lays out one session's screen. Pure.
func sessionPageFor(project string, s SessionView) SessionScreenView {
	k := sessionKind(s)
	return SessionScreenView{
		Project:      project,
		SessionFacts: factsOf(s, k),
		Page: sessionPage{
			Title: titleOf(s), Badges: sessionBadges(s, k), Lines: sessionLines(s, k),
			KV: sessionKV(s), Summary: sessionSummary(s),
		},
	}
}

// phaseWord is the Phase value of the kv list: the phase, or why there is
// none.
func phaseWord(s SessionView) string {
	switch {
	case s.Problem != "":
		return "none: this is not a session"
	case s.Scratch:
		return "none: a one-off session has no phases"
	case s.StatusStale || s.Phase == "":
		return "unknown"
	}
	return s.Phase
}

// statusKnown reports whether status.json's facts (loop count, updated) mean
// anything for the row.
func statusKnown(s SessionView) bool {
	return s.Problem == "" && !s.Scratch && !s.StatusStale
}

// sessionKV is the session screen's kv list.
func sessionKV(s SessionView) []kvRow {
	loop, updated := "unknown", "unknown"
	if statusKnown(s) {
		loop = strconv.Itoa(s.LoopCount)
		if !s.UpdatedAt.IsZero() {
			updated = whenUTC(s.UpdatedAt)
		}
	} else if s.Scratch || s.Problem != "" {
		loop, updated = "none", "none"
	}
	started := "unknown"
	if !s.StartedAt.IsZero() {
		started = whenUTC(s.StartedAt)
	}
	label := s.Label
	if label == "" {
		label = "none"
	}
	branch := "none"
	switch {
	case s.Branch != "" && s.BaseBranch != "":
		branch = s.Branch + " → " + s.BaseBranch
	case s.Branch != "":
		branch = s.Branch
	}
	folder := s.WorktreePath
	if folder == "" {
		folder = "unknown"
	}
	return []kvRow{
		{Label: "Phase", Value: phaseWord(s)},
		{Label: "Loop count", Value: loop},
		{Label: "Started", Value: started},
		{Label: "Updated", Value: updated},
		{Label: "Label", Value: label},
		{Label: "Branch", Value: branch},
		{Label: "Folder", Value: folder, Code: s.WorktreePath != ""},
		{Label: "Host", Value: hostWord(s)},
	}
}

// hostWord is the Host value of the kv list.
func hostWord(s SessionView) string {
	switch {
	case s.Problem != "":
		return "none"
	case !s.HostRunning:
		return "not running"
	case s.Wedged:
		return "running, but its agent has gone quiet"
	}
	return "running"
}

// phaseSentences say what each phase means for the owner.
var phaseSentences = map[string]string{
	"planning":  "It is planning its work.",
	"executing": "It is carrying out its work plan.",
	"reviewing": "It is reviewing its own work.",
	"fixing":    "It is fixing what its review found.",
	"done":      "It has finished.",
	"failed":    "It has stopped after a failure.",
}

// sessionSummary is the overview in sentences: the phase, what the session
// waits for, its work plan, its review and its host. A worktree problem has
// only its reason and a one-off has no plan or review to speak of, so their
// summaries are shorter rather than full of "none".
func sessionSummary(s SessionView) []string {
	if s.Problem != "" {
		return []string{"This is not a session but a problem with a worktree: " + strings.TrimSuffix(s.Problem, ".") + ".", "afk clean can tidy it up."}
	}
	var out []string
	switch {
	case s.Scratch:
		out = append(out, "It is a one-off session with no plan, so it has no phase, work plan or review.")
	case s.StatusStale:
		out = append(out, "Its status.json could not be read ("+strings.TrimSuffix(reasonText(s.StaleReason), ".")+"), so its phase is unknown.")
	default:
		if p, ok := phaseSentences[s.Phase]; ok {
			out = append(out, p)
		} else {
			out = append(out, fmt.Sprintf("Its phase is %q, which this version of afk does not know.", s.Phase))
		}
		if waitsForYou(s) {
			out = append(out, "It is waiting for you: open its terminal to answer.")
		} else if s.Phase != "done" && s.Phase != "failed" {
			out = append(out, "It is not waiting for you.")
		}
	}
	if !s.Scratch {
		switch {
		case s.PlanError != "":
			out = append(out, "Its work plan could not be read: "+strings.TrimSuffix(s.PlanError, ".")+".")
		case s.PlanTotal > 0:
			p := fmt.Sprintf("%d of %d work-plan tasks done", s.PlanDone, s.PlanTotal)
			if s.PlanFailed > 0 {
				p += fmt.Sprintf(" (%d failed)", s.PlanFailed)
			}
			out = append(out, p+".")
		default:
			out = append(out, "It has no work plan yet.")
		}
		out = append(out, verdictSentence(s))
	}
	switch {
	case !s.HostRunning:
		out = append(out, "Its host is not running.")
	case s.Wedged:
		out = append(out, "Its host is running, but its agent has gone quiet.")
	default:
		out = append(out, "Its host is running.")
	}
	return out
}

// verdictSentence is the review in a sentence, with each check.
func verdictSentence(s SessionView) string {
	if s.VerdictError != "" {
		return "Its review verdict could not be read: " + strings.TrimSuffix(s.VerdictError, ".") + "."
	}
	var lead string
	switch s.Verdict {
	case "APPROVE":
		lead = "Its review approved the work"
	case "REJECT":
		lead = "Its review asked for changes"
	case "":
		return "Not reviewed yet."
	default:
		lead = fmt.Sprintf("Its review says %q", s.Verdict)
	}
	if len(s.VerdictChecks) == 0 {
		return lead + "."
	}
	checks := make([]string, 0, len(s.VerdictChecks))
	for _, c := range s.VerdictChecks {
		checks = append(checks, strings.ToLower(c.Name)+" "+c.Result)
	}
	return lead + ": " + strings.Join(checks, " · ") + "."
}

func (s *server) sessionsList(w http.ResponseWriter, r *http.Request) {
	p, ok := s.findProject(w, r)
	if !ok {
		return
	}
	s.respond(w, r, http.StatusOK, "sessions", sessionsPageFor(p.Name, s.d.Sessions(p.Root)))
}

// session is one session's screen. The first row with the name wins. Names
// are not unique across discovery's rows: a one-off (found by its
// host.json) and a planned session (found by its status.json) can share
// one. For one name, discovery's order (internal/discover's
// sortSessionStates) puts the status-backed row first, and every other
// reader of a name (`afk attach`, the dashboard) takes the first match, so
// the page does the same and one name never means two sessions depending on
// where it was typed. The address has no way to name the other row; it
// stays visible on the list.
func (s *server) session(w http.ResponseWriter, r *http.Request) {
	p, ok := s.findProject(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	for _, sv := range s.d.Sessions(p.Root) {
		if sv.Name == name {
			s.respond(w, r, http.StatusOK, "session", sessionPageFor(p.Name, sv))
			return
		}
	}
	s.fail(w, r, http.StatusNotFound, "There is no session with that name in this project. It may have been cleaned up. Check the address, or go back to the project's sessions and pick it from the list.")
}
