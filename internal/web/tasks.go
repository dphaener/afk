package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/nexttask"
	"github.com/dphaener/afk/internal/planlint"
	"github.com/dphaener/afk/internal/taskstatus"
)

// This file is the Tasks screens (docs/design-system.md §3 "Tasks"): a
// project's master-plan tasks with their status and what they wait for, as
// `afk tasks` prints them, and one task's screen with why it is in that state
// and its tokens, as `afk tokens <task>` reports them. Both only read.

// The ?show= values of the tasks list.
const (
	showOpen = "open"
	showDone = "done"
	showAll  = "all"
)

// noMilestone is the section title for tasks above every milestone heading,
// or every task when the plan cannot be read.
const noMilestone = "Tasks"

// TasksView is a project's task list. Done and Total count the whole plan
// the way the project's progress does (superseded tasks left out); Sections
// hold only the tasks the filter shows.
type TasksView struct {
	Project string `json:"project"`
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	// Show is the filter in force: "open", "done" or "all".
	Show     string        `json:"show"`
	Sections []TaskSection `json:"sections"`
	// Problem is why the plan or the ledger could not be read, and empty
	// otherwise. The counts and sections are then empty and mean nothing.
	Problem string `json:"problem"`
	// Empty is the empty state's sentence when no section is left under the
	// filter, and Filters the Open / Done / All links. Page only.
	Empty   string       `json:"-"`
	Filters []taskFilter `json:"-"`
}

// TaskSection is one milestone of the list. Done and Total count every task
// under the milestone, whatever the filter, so "2 of 7 done" does not change
// when the owner switches from Open to All.
type TaskSection struct {
	Title string    `json:"title"`
	Done  int       `json:"done"`
	Total int       `json:"total"`
	Tasks []TaskRow `json:"tasks"`
	// Anchor is the heading's id on the page.
	Anchor string `json:"-"`
}

// TaskRow is one task of the list. Status is the ledger's word; ReplacedBy
// is set for a superseded task and Unmet for a task that is neither done nor
// superseded. Both are never null.
type TaskRow struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Status     string   `json:"status"`
	Unmet      []string `json:"unmet"`
	ReplacedBy []string `json:"replaced_by"`
	// Href, Word, Badge and Waits are how the page draws the row: Word is the
	// status as `afk tasks` prints it ("superseded (by T3.4a, T3.4b)"),
	// Waits the "Waits for T3.4, T3.5" line or "".
	Href  string `json:"-"`
	Word  string `json:"-"`
	Badge string `json:"-"`
	Waits string `json:"-"`
}

// taskFilter is one link of the segmented Open / Done / All control.
type taskFilter struct {
	Label, Href string
	Current     bool
}

// TaskView is one task's screen.
type TaskView struct {
	Project   string   `json:"project"`
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Status    string   `json:"status"`
	DependsOn []string `json:"depends_on"`
	Unmet     []string `json:"unmet"`
	// Why is one plain line on why the task is in its state.
	Why    string     `json:"why"`
	Tokens TaskTokens `json:"tokens"`
	// Page is the task laid out for the page; the JSON leaves it out.
	Page taskPage `json:"-"`
}

// taskPage is what task.html draws besides the view's own fields.
type taskPage struct {
	// Word and Badge are the status as the list draws it.
	Word, Badge string
	// Depends is the dependencies line.
	Depends string
	Tokens  tokensCard
	// Sheet is the Set status… sheet, or nil when this daemon was given no
	// way to set a status (Deps.SetTaskStatus), and then no action is drawn.
	Sheet *statusSheet
}

// tokensCard is the Tokens card laid out.
type tokensCard struct {
	// Recorded is true when there is a record to draw; Message is the
	// sentence shown instead otherwise, and Error marks it as a fault.
	Recorded bool
	Message  string
	Error    bool
	// Total is the section head's meta, e.g. "2.9M weighted".
	Total    string
	Segments []splitSegment
	// Label is the split bar's spoken description: every phase's share.
	Label  string
	Legend []legendRow
	Roles  []roleRow
}

// splitSegment is one part of the split bar: its phase's class and its width
// in whole percent (the w-N class in page.css).
type splitSegment struct {
	Class string
	Width int
}

// legendRow is one phase under the split bar.
type legendRow struct {
	Phase, Class, Value, Share string
}

// roleRow is one row of the role table, already formatted.
type roleRow struct {
	Role, Calls, Input, Output, Weighted string
}

// readShow turns the ?show= value into a filter. Anything that is not one of
// the three words reads as open, the default: a mistyped address still shows
// a list rather than an error, and the value is never echoed back.
func readShow(v string) string {
	switch v {
	case showDone, showAll:
		return v
	}
	return showOpen
}

// shownBy reports whether a task with status st is in the list under show.
// Superseded counts as done: its work is finished in the tasks that replace
// it, so it is not something still to do.
func shownBy(show string, st ledger.Status) bool {
	finished := st == ledger.Done || st == ledger.Superseded
	switch show {
	case showAll:
		return true
	case showDone:
		return finished
	}
	return !finished
}

// statusWord is a task's status as `afk tasks` prints it: the ledger's word,
// with the replacements of a superseded task named.
func statusWord(st ledger.Status, e ledger.Entry) string {
	if st == ledger.Superseded && len(e.ReplacedBy) > 0 {
		return fmt.Sprintf("superseded (by %s)", strings.Join(e.ReplacedBy, ", "))
	}
	return string(st)
}

// statusBadge is the design system's task-status badge class. A word this
// binary does not know gets the plain badge, so it still shows as written.
func statusBadge(st ledger.Status) string {
	for _, known := range ledger.Statuses() {
		if st == known {
			return "badge--" + string(st)
		}
	}
	return "badge--plain"
}

// unmetOf is what a task still waits for: nexttask.Unmet, the rule afk next
// applies, for a task that is neither done nor superseded, and nothing for
// those two (what they once waited for no longer matters, as `afk tasks`
// says with its "-"). Never nil.
func unmetOf(t masterplan.Task, st ledger.Status, l ledger.Ledger) []string {
	if st == ledger.Done || st == ledger.Superseded {
		return []string{}
	}
	u := nexttask.Unmet(t, l)
	if u == nil {
		return []string{}
	}
	return u
}

// tasksBase is the address of a project's tasks list.
func tasksBase(project string) string {
	return "/p/" + url.PathEscape(project) + "/tasks"
}

// tasksPageFor lays out a project's task list. Pure: the handler reads the
// plan's text, the tasks and the ledger and passes them in.
//
// Each task goes under the nearest milestone heading above it, by the rule
// the plan checker counts milestones with (planlint.MilestoneHeading), so the
// list and `afk plan lint` agree on what the milestones are. A task's line is
// taken from planData's own scan, matched by id, and from the task itself
// when the scan does not hold it; a task above every milestone, or every task
// when planData cannot be read, goes in one "Tasks" section first.
func tasksPageFor(project string, planData []byte, tasks []masterplan.Task, l ledger.Ledger, show string) TasksView {
	show = readShow(show)
	v := TasksView{Project: project, Show: show, Sections: []TaskSection{}}
	base := tasksBase(project)
	for _, f := range []struct{ label, show string }{{"Open", showOpen}, {"Done", showDone}, {"All", showAll}} {
		v.Filters = append(v.Filters, taskFilter{Label: f.label, Href: base + "?show=" + f.show, Current: f.show == show})
	}

	outline := masterplan.Scan(planData)
	var milestones []masterplan.Heading
	for _, h := range outline.Headings {
		if planlint.MilestoneHeading(h) {
			milestones = append(milestones, h)
		}
	}
	lineOf := make(map[string]int, len(outline.Tasks))
	for _, t := range outline.Tasks {
		lineOf[t.ID] = t.Line
	}

	// Section 0 is "Tasks"; section i+1 is milestones[i]. All are built and
	// the empty ones dropped at the end, so the order is the plan's.
	all := make([]TaskSection, len(milestones)+1)
	all[0].Title = noMilestone
	for i, h := range milestones {
		all[i+1].Title = h.Text
	}
	for _, t := range tasks {
		line, ok := lineOf[t.ID]
		if !ok {
			line = t.Line
		}
		// The last milestone heading at or above the task's line.
		idx := sort.Search(len(milestones), func(i int) bool { return milestones[i].Line > line })
		sec := &all[idx]

		st := l.Status(t.ID)
		e, _ := l.Get(t.ID)
		switch st {
		case ledger.Superseded:
			// Left out of the count, as the project's progress does: its
			// work moved to the tasks that replace it.
		case ledger.Done:
			sec.Done++
			sec.Total++
			v.Done++
			v.Total++
		default:
			sec.Total++
			v.Total++
		}
		if !shownBy(show, st) {
			continue
		}
		row := TaskRow{
			ID: t.ID, Title: t.Title, Status: string(st),
			Unmet: unmetOf(t, st, l), ReplacedBy: []string{},
			Href: base + "/" + url.PathEscape(t.ID), Word: statusWord(st, e), Badge: statusBadge(st),
		}
		if st == ledger.Superseded && len(e.ReplacedBy) > 0 {
			row.ReplacedBy = append(row.ReplacedBy, e.ReplacedBy...)
		}
		if len(row.Unmet) > 0 {
			row.Waits = "Waits for " + strings.Join(row.Unmet, ", ")
		}
		sec.Tasks = append(sec.Tasks, row)
	}
	for i, sec := range all {
		if len(sec.Tasks) == 0 {
			continue
		}
		sec.Anchor = "tasks-section-" + strconv.Itoa(i)
		v.Sections = append(v.Sections, sec)
	}
	if len(v.Sections) == 0 {
		switch {
		case len(tasks) == 0:
			v.Empty = "No tasks in the plan"
		case show == showDone:
			v.Empty = "Nothing done yet"
		case show == showOpen:
			v.Empty = "No open tasks"
		default:
			v.Empty = "No tasks in the plan"
		}
	}
	return v
}

// taskPageFor lays out one task's screen from the task, its ledger entry and
// its token record. Pure.
func taskPageFor(project string, t masterplan.Task, l ledger.Ledger, tok TaskTokens) TaskView {
	st := l.Status(t.ID)
	e, _ := l.Get(t.ID)
	v := TaskView{
		Project: project, ID: t.ID, Title: t.Title, Status: string(st),
		DependsOn: []string{}, Unmet: unmetOf(t, st, l), Tokens: tok,
	}
	v.DependsOn = append(v.DependsOn, t.DependsOn...)
	if v.Tokens.Phases == nil {
		v.Tokens.Phases = []TokenPhase{}
	}
	if v.Tokens.Roles == nil {
		v.Tokens.Roles = []TokenRole{}
	}
	v.Why = whyLine(t.ID, st, e, v.Unmet)
	v.Page = taskPage{Word: statusWord(st, e), Badge: statusBadge(st), Depends: dependsLine(t.DependsOn), Tokens: tokensCardFor(v.Tokens)}
	return v
}

// whyLine is the one line on why a task is in its state, and what moves it on.
func whyLine(id string, st ledger.Status, e ledger.Entry, unmet []string) string {
	switch st {
	case ledger.Pending:
		if len(unmet) > 0 {
			return fmt.Sprintf("Waiting for %s to be done first.", strings.Join(unmet, ", "))
		}
		return "Ready: afk starts it when nothing else is running."
	case ledger.Running:
		s := "Running"
		if e.Session != "" {
			s += " in session " + e.Session
		}
		if e.Attempts > 0 {
			s += fmt.Sprintf(", attempt %d", e.Attempts)
		}
		return s + "."
	case ledger.Shipping:
		if e.Ship == "" {
			return "Shipping; its first step is not recorded yet."
		}
		return "Shipping: " + strings.ReplaceAll(e.Ship, "_", " ") + "."
	case ledger.Parked:
		switch n := len(e.ParkedOn); n {
		case 0:
			return "Parked, waiting on no inbox item, so only a change to its status starts it again."
		case 1:
			return "Parked, waiting on 1 inbox item."
		default:
			return fmt.Sprintf("Parked, waiting on %d inbox items.", n)
		}
	case ledger.Done:
		return "Done."
	case ledger.Failed:
		return "Failed. The build loop will not start it again until its status is set back to pending."
	case ledger.Superseded:
		if len(e.ReplacedBy) == 0 {
			return "Superseded. No replacing tasks were recorded."
		}
		return fmt.Sprintf("Superseded by %s, which do its work instead.", strings.Join(e.ReplacedBy, ", "))
	}
	return fmt.Sprintf("%s has the status %q, which this version of afk does not know.", id, string(st))
}

// dependsLine says what a task depends on in the plan.
func dependsLine(deps []string) string {
	if len(deps) == 0 {
		return "Depends on no other task."
	}
	return "Depends on " + strings.Join(deps, ", ") + "."
}

// knownPhases are the token ledger's phases, which have their own colour in
// page.css; any other phase is drawn in the "other" colour.
var knownPhases = map[string]bool{"plan": true, "execute": true, "review": true, "fix": true, "ship": true}

func phaseClass(phase string) string {
	if knownPhases[phase] {
		return phase
	}
	return "other"
}

// tokensCardFor lays out the Tokens card from the record.
func tokensCardFor(tok TaskTokens) tokensCard {
	switch tok.State {
	case TokensRecorded:
	case TokensNone:
		return tokensCard{Message: "No tokens recorded for this task yet."}
	default:
		msg := tok.Err
		if msg == "" {
			msg = "The token record could not be read."
		}
		return tokensCard{Message: msg, Error: true}
	}
	c := tokensCard{Recorded: true, Total: tok.Short}
	if c.Total == "" {
		c.Total = wholeNumber(tok.Total) + " weighted"
	}
	weights := make([]float64, len(tok.Phases))
	for i, p := range tok.Phases {
		weights[i] = p.Weighted
	}
	widths := splitWidths(weights)
	var spoken []string
	for i, p := range tok.Phases {
		cls := phaseClass(p.Phase)
		share := shareText(p.Share)
		if widths[i] > 0 {
			c.Segments = append(c.Segments, splitSegment{Class: cls, Width: widths[i]})
		}
		c.Legend = append(c.Legend, legendRow{Phase: p.Phase, Class: cls, Value: wholeNumber(p.Weighted), Share: share})
		spoken = append(spoken, p.Phase+" "+share)
	}
	c.Label = "Tokens by phase: " + strings.Join(spoken, ", ")
	if len(spoken) == 0 {
		c.Label = "Tokens by phase: none recorded"
	}
	for _, r := range tok.Roles {
		c.Roles = append(c.Roles, roleRow{
			Role: r.Role, Calls: strconv.FormatInt(r.Calls, 10), Input: strconv.FormatInt(r.Input, 10),
			Output: strconv.FormatInt(r.Output, 10), Weighted: wholeNumber(r.Weighted),
		})
	}
	return c
}

// wholeNumber prints a weighted count the way the command does (%.0f).
func wholeNumber(f float64) string { return fmt.Sprintf("%.0f", f) }

// shareText prints a share as the command's share column does: one decimal
// and a percent sign.
func shareText(f float64) string { return fmt.Sprintf("%.1f%%", f) }

// splitWidths turns weights into whole-percent widths for the split bar,
// summing to exactly 100 when any weight is above zero (all zeros otherwise).
//
// The widths are classes (w-0 … w-100 in page.css) because the page carries
// no style attribute, so they have to be whole numbers. Plain rounding can
// add up to 99 or 101, which leaves a gap or pushes the last segment out of
// the bar; the largest-remainder method hands the spare points to the parts
// that lost most in rounding, ties to the earlier part. A part with a weight
// but under half a percent would then be invisible, so it gets 1 and the
// point is taken from the widest part, keeping the sum at 100: the bar must
// show every phase the legend lists as having cost something.
func splitWidths(weights []float64) []int {
	out := make([]int, len(weights))
	var total float64
	for _, w := range weights {
		if w > 0 {
			total += w
		}
	}
	if total <= 0 {
		return out
	}
	rem := make([]float64, len(weights))
	sum := 0
	for i, w := range weights {
		if w <= 0 {
			continue
		}
		exact := w / total * 100
		out[i] = int(exact)
		rem[i] = exact - float64(out[i])
		sum += out[i]
	}
	order := make([]int, 0, len(weights))
	for i, w := range weights {
		if w > 0 {
			order = append(order, i)
		}
	}
	sort.SliceStable(order, func(a, b int) bool { return rem[order[a]] > rem[order[b]] })
	for k := 0; sum < 100; k++ {
		out[order[k%len(order)]]++
		sum++
	}
	for i, w := range weights {
		if w > 0 && out[i] == 0 {
			widest := 0
			for j := range out {
				if out[j] > out[widest] {
					widest = j
				}
			}
			out[widest]--
			out[i] = 1
		}
	}
	return out
}

// findTask returns the task the address names from tasks. An id that is not
// shaped like a task id, or that the plan does not hold, is answered here
// with the same 404 an unknown project gets.
func (s *server) findTask(w http.ResponseWriter, r *http.Request, tasks []masterplan.Task) (masterplan.Task, bool) {
	id := r.PathValue("id")
	if masterplan.ValidID(id) {
		for _, t := range tasks {
			if t.ID == id {
				return t, true
			}
		}
	}
	s.fail(w, r, http.StatusNotFound, "There is no task with that id in this project's plan. Check the address, or go back to the project's tasks and pick it from the list.")
	return masterplan.Task{}, false
}

// readPlanData is the plan's text for grouping the list by milestone, or nil
// when it cannot be read: the list then shows every task in one section
// rather than failing, since the tasks themselves came from LoadBuild.
func (s *server) readPlanData(root string) []byte {
	if s.d.ReadPlan == nil {
		return nil
	}
	_, data, err := s.d.ReadPlan(root)
	if err != nil {
		return nil
	}
	return data
}

func (s *server) tasksList(w http.ResponseWriter, r *http.Request) {
	p, ok := s.findProject(w, r)
	if !ok {
		return
	}
	tasks, l, err := s.d.LoadBuild(p.Root)
	show := r.URL.Query().Get("show")
	if err != nil {
		// Shown as the page's problem, as on the project page: the rest of
		// the page still works, and guessing a list from half the data would
		// mislead.
		v := tasksPageFor(p.Name, nil, nil, ledger.Ledger{}, show)
		v.Problem = err.Error()
		v.Empty = ""
		s.respond(w, r, http.StatusOK, "tasks", v)
		return
	}
	s.respond(w, r, http.StatusOK, "tasks", tasksPageFor(p.Name, s.readPlanData(p.Root), tasks, l, show))
}

func (s *server) task(w http.ResponseWriter, r *http.Request) {
	p, ok := s.findProject(w, r)
	if !ok {
		return
	}
	tasks, l, err := s.d.LoadBuild(p.Root)
	if err != nil {
		// Without the plan there is no telling whether the task exists, so
		// unlike the list this cannot show the rest of the page.
		s.fail(w, r, http.StatusInternalServerError, fmt.Sprintf("The plan or the ledger could not be read: %v.", strings.TrimSuffix(err.Error(), ".")))
		return
	}
	t, ok := s.findTask(w, r, tasks)
	if !ok {
		return
	}
	tok := TaskTokens{State: TokensUnreadable, Err: "This afk daemon was started without a way to read token records, so the tokens cannot be shown here. Run afk tokens " + t.ID + " in the project folder."}
	if s.d.TaskTokens != nil {
		tok = s.d.TaskTokens(p.Root, t.ID)
	}
	v := taskPageFor(p.Name, t, l, tok)
	if s.d.SetTaskStatus != nil && !isAPI(r.URL.Path) {
		sh := statusSheetFor(p.Name, t.ID, v.Status)
		v.Page.Sheet = &sh
	}
	s.respond(w, r, http.StatusOK, "task", v)
}

// ---- Setting a task's status ----
//
// The words here all come from internal/taskstatus, the package the
// dashboard's Set status action uses, so the page and the dashboard say the
// same thing before a change; the answer after it is the command's own Text,
// passed through Deps.SetTaskStatus. This package never writes the ledger.

// statusSheetID is the dialog's id, which the Set status… link names.
const statusSheetID = "confirm-status"

// idsPlaceholder stands where the replacing ids go in superseded's sentence
// as the sheet carries it: confirm.js puts the typed ids in its place. It is
// put through taskstatus.ConfirmText as if it were the one id, so the script
// fills in text the shared package wrote and never words of its own.
const idsPlaceholder = "{ids}"

// statusSheet is the Set status… sheet on the task screen, and the choices
// of the no-JS page: one set of values for both, so they cannot drift.
type statusSheet struct {
	// ID is the dialog's id; Title the question naming the task.
	ID, Title string
	// Path is the POST address and Page the no-JS page's address, which the
	// Set status… link goes to when the sheet cannot open.
	Path, Page string
	// Current is the task's status as read for this page.
	Current string
	Choices []statusChoice
	// Safe is the harmless button's label.
	Safe string
	// IDsHint is the replacing-ids field's hint and EmptyIDs the sentence for
	// superseded with no ids typed.
	IDsHint, EmptyIDs string
}

// statusChoice is one status to pick, with what the confirm says for it.
type statusChoice struct {
	Status string
	// Current marks the task's status now; it is checked to start with.
	Current bool
	// Action is the action button's label for this choice ("Mark done").
	Action string
	// Lines are the confirm's sentences for this choice, one to a line.
	Lines []string
	// IDsLines is superseded's sentences with idsPlaceholder where the
	// replacing ids go, for the script; empty for every other status.
	IDsLines []string
}

// Superseded reports whether this is the choice that needs replacing ids.
func (c statusChoice) Superseded() bool { return c.Status == string(ledger.Superseded) }

// statusLines is what the confirm says for setting task id from old to to,
// one sentence to a line. The current status says "already", as the
// dashboard's picker does, except superseded: its replacing ids may be what
// the owner wants to change, so it gets the full confirm like any change.
// The sheet and the no-JS page both call this, which is what keeps them in
// the same words for the same choice.
func statusLines(id, old, to string, replacedBy []string) []string {
	if to == old && to != string(ledger.Superseded) {
		return []string{taskstatus.Already(id, old)}
	}
	return strings.Split(taskstatus.ConfirmText(id, old, to, replacedBy), "\n")
}

// taskBase is the address of one task's screen.
func taskBase(project, id string) string {
	return tasksBase(project) + "/" + url.PathEscape(id)
}

// statusSheetFor builds the sheet for task id whose status is now current.
// Pure. Every status is offered, none disabled: the ledger is the owner's to
// repair, and the sentence says what follows from each choice.
func statusSheetFor(project, id, current string) statusSheet {
	base := taskBase(project, id)
	sh := statusSheet{
		ID: statusSheetID, Title: "Set the status of " + id + "?",
		Path: base + "/status", Page: base + "/status", Current: current,
		Safe:     "Leave it as it is",
		IDsHint:  "Ids of the tasks that replace it, for example T3.4a, T3.4b",
		EmptyIDs: taskstatus.EmptyIDsPrompt(id),
	}
	for _, st := range taskstatus.Choices() {
		c := statusChoice{Status: st, Current: st == current, Action: taskstatus.ActionLabel(st), Lines: statusLines(id, current, st, nil)}
		if c.Superseded() {
			c.IDsLines = statusLines(id, current, st, []string{idsPlaceholder})
		}
		sh.Choices = append(sh.Choices, c)
	}
	return sh
}

// TaskStatusPageView is the no-JS page behind Set status…, in two steps: with
// no choice yet (Chosen empty) it shows the choices in a GET form back to
// itself; with a choice it shows that choice's sentences and a POST form
// carrying the confirm. It has no JSON form.
type TaskStatusPageView struct {
	Project, ID, Title string
	Sheet              statusSheet
	// Chosen is the status picked in step one, set only for step two, and
	// Choice its entry in Sheet.Choices with the typed ids in its sentences.
	Chosen string
	Choice statusChoice
	// ReplacedBy is the typed ids as one field value, carried to the POST.
	ReplacedBy string
	// Checked is the radio checked in step one: the current status, or the
	// one the owner picked before being sent back.
	Checked string
	// Notice is why step one is shown again: the command's words for an
	// unknown status, or the prompt for the missing replacing ids.
	Notice string
	// Back is step two's Back link, to step one.
	Back string
}

// taskStatusPageFor is the pure decision behind the no-JS page. to and ids
// are the query's values; current is the task's status now.
func taskStatusPageFor(project string, t masterplan.Task, current, to, ids string) TaskStatusPageView {
	sh := statusSheetFor(project, t.ID, current)
	v := TaskStatusPageView{Project: project, ID: t.ID, Title: t.Title, Sheet: sh, Checked: current, Back: sh.Page}
	if to == "" {
		return v
	}
	st, err := ledger.ParseStatus(to)
	if err != nil {
		// The command's own words, so the page says what `afk ledger set`
		// would; the value is escaped by the template like everything else.
		v.Notice = err.Error() + "."
		return v
	}
	replacedBy := taskstatus.SplitIDs(ids)
	if st == ledger.Superseded && len(replacedBy) == 0 {
		v.Checked = string(st)
		v.Notice = taskstatus.EmptyIDsPrompt(t.ID)
		return v
	}
	if st != ledger.Superseded {
		// The ids field is on the form whatever is picked; ids typed and
		// then a different status chosen are not a request to supersede.
		replacedBy = nil
	}
	v.Chosen = string(st)
	v.Choice = statusChoice{Status: string(st), Current: string(st) == current, Action: taskstatus.ActionLabel(string(st)), Lines: statusLines(t.ID, current, string(st), replacedBy)}
	v.ReplacedBy = strings.Join(replacedBy, " ")
	return v
}

// taskStatusPage is the no-JS page. It is not in routes: it has no JSON
// form, so it must not get an /api twin. Without the status seam it is the
// catch-all 404, as the POST is, since there is nothing to confirm.
func (s *server) taskStatusPage(w http.ResponseWriter, r *http.Request) {
	p, ok := s.findProject(w, r)
	if !ok {
		return
	}
	if s.d.SetTaskStatus == nil {
		s.notFound(w, r)
		return
	}
	tasks, l, err := s.d.LoadBuild(p.Root)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, fmt.Sprintf("The plan or the ledger could not be read: %v.", strings.TrimSuffix(err.Error(), ".")))
		return
	}
	t, ok := s.findTask(w, r, tasks)
	if !ok {
		return
	}
	q := r.URL.Query()
	s.renderPage(w, r, http.StatusOK, "task_status", taskStatusPageFor(p.Name, t, string(l.Status(t.ID)), q.Get("to"), q.Get("replaced_by")))
}

// TaskStatusView is the API's answer to a status change: the task as it
// reads after the change, with what the change did beside it.
type TaskStatusView struct {
	TaskView
	Result TaskStatusResult `json:"result"`
}

// The outcomes Deps.SetTaskStatus reports.
const (
	StatusChanged = "changed"
	StatusAlready = "already"
	StatusRefused = "refused"
	StatusFailed  = "failed"
)

// statusRequest is what a status POST asks for.
type statusRequest struct {
	Status     string
	ReplacedBy []string
}

// readStatusRequest takes the status and the replacing ids from a body
// already read. A form's replaced_by is typed text, split as the dashboard
// splits it; JSON's is a list of ids.
func readStatusRequest(b postBody) (statusRequest, error) {
	if b.contentType == "application/json" {
		var body struct {
			Status     string   `json:"status"`
			ReplacedBy []string `json:"replaced_by"`
		}
		if err := json.Unmarshal(b.json, &body); err != nil {
			return statusRequest{}, err
		}
		return statusRequest{Status: body.Status, ReplacedBy: body.ReplacedBy}, nil
	}
	return statusRequest{
		Status:     b.form.Get("status"),
		ReplacedBy: taskstatus.SplitIDs(strings.Join(b.form["replaced_by"], " ")),
	}, nil
}

// setTaskStatus is the status POST. The confirm is checked first and nothing
// else happens without it; then the seam is called exactly once and its
// outcome decides the answer. The ledger is written behind the seam, by the
// same code as `afk ledger set`, never here.
func (s *server) setTaskStatus(w http.ResponseWriter, r *http.Request) {
	p, ok := s.findProject(w, r)
	if !ok {
		return
	}
	if s.d.SetTaskStatus == nil {
		s.notFound(w, r)
		return
	}
	b, err := readPostBody(r)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			s.tooLarge(w, r)
			return
		}
	}
	if err != nil || !b.confirmed() {
		s.fail(w, r, http.StatusBadRequest, "Nothing changed: setting a task's status needs the confirm. Open the task and press Set status…")
		return
	}
	req, err := readStatusRequest(b)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, `Nothing changed: the request could not be read. Send it as JSON like {"status": "pending", "replaced_by": [], "confirm": true}.`)
		return
	}
	id := r.PathValue("id")
	res := s.d.SetTaskStatus(p.Root, id, req.Status, req.ReplacedBy)
	switch res.Outcome {
	case StatusChanged, StatusAlready:
	case StatusRefused:
		s.fail(w, r, http.StatusBadRequest, res.Text)
		return
	case StatusFailed:
		s.fail(w, r, http.StatusInternalServerError, res.Text)
		return
	default:
		s.fail(w, r, http.StatusInternalServerError, "The status change gave an answer this page does not know. This is a fault in afk; please report it. Run afk tasks in the project folder to see what the ledger holds.")
		return
	}
	if !isAPI(r.URL.Path) {
		code := "status"
		if res.Outcome == StatusAlready {
			code = "already"
		}
		w.Header().Set("Location", taskBase(p.Name, id)+"?done="+code)
		w.WriteHeader(http.StatusSeeOther)
		return
	}
	// The task as it reads now, from the files, so the caller sees what is
	// true after the change and not what was asked for.
	tasks, l, err := s.d.LoadBuild(p.Root)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, fmt.Sprintf("The status was set, but the plan or the ledger could not be read back: %v.", strings.TrimSuffix(err.Error(), ".")))
		return
	}
	for _, t := range tasks {
		if t.ID == id {
			tok := TaskTokens{State: TokensUnreadable, Err: "This afk daemon was started without a way to read token records."}
			if s.d.TaskTokens != nil {
				tok = s.d.TaskTokens(p.Root, t.ID)
			}
			s.respond(w, r, http.StatusOK, "task", TaskStatusView{TaskView: taskPageFor(p.Name, t, l, tok), Result: res})
			return
		}
	}
	s.fail(w, r, http.StatusInternalServerError, "The status was set, but the task is no longer in the plan. Run afk tasks in the project folder.")
}

// taskToast is the toast on a task's screen after a status change. Only the
// two codes the status POST leads to count, and the id and status are the
// server's own read of the plan and the ledger, never the request's, so the
// address cannot make the page say anything the ledger does not.
func taskToast(v TaskView, code string) string {
	if code != "status" && code != "already" {
		return ""
	}
	text := toastTexts[code]
	return strings.NewReplacer("{task}", v.ID, "{status}", v.Status).Replace(text)
}
