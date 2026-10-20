package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/dphaener/afk/internal/planlint"
	"github.com/dphaener/afk/internal/plansession"
)

// This file is the plan page's planning status and its Approve the plan…
// action (T8.11). T5.7 kept approving off the page; the owner has since put
// every owner-only action on the page behind a confirm, and approving is one
// of them.
//
// The package still never approves anything itself. Deps.ApprovePlan is the
// root package's print-free entry point beside `afk plan approve` (the one
// the dashboard's approve uses), and Deps.PlanStatus is the root's read of
// .afk/plan/ worded as the dashboard words it. Both take the project's main
// checkout, which is looked up from the address's project name among the
// configured projects and the review list: no request names a folder or a
// plan file.
//
// The handler does not decide whether the plan may be approved before it
// calls the seam. The entry point re-reads the planning session, the plan
// and the checker and refuses on its own, so a page drawn a minute ago that
// still shows the button gets that plain refusal; a second check here would
// only be a second copy of the rule that could drift from the first.

// PlanStatus is the planning status of a project's plan, as Deps.PlanStatus
// reports it. It is this package's own plain copy of the dashboard's planning
// status, because this package never imports internal/ui; the root fills it.
// Every key is always present in the JSON, and problems is an empty list
// rather than null, so a reader can tell "no problems" from "not asked".
type PlanStatus struct {
	// Known is false when the status could not be read at all, or when this
	// daemon has no way to read it; the other fields then mean nothing.
	Known bool `json:"known"`
	// Session is true when there is a planning session (.afk/plan/state.json).
	Session bool `json:"session"`
	// Damaged is true when the planning session's state file is there but
	// cannot be read as one.
	Damaged bool `json:"damaged"`
	// Mode is the session's mode (new, extend, amend, reconcile, fix) and Phase
	// its effective phase, the one afk plan status reports.
	Mode  string `json:"mode"`
	Phase string `json:"phase"`
	// Line is the plan line already worded by the root, exactly as the
	// dashboard shows it, so the two surfaces say the same words.
	Line string `json:"line"`
	// Approvable is true for a new session at review by the effective phase:
	// the one state afk plan approve accepts.
	Approvable bool `json:"approvable"`
	// Problems are the status reader's own sentences about what it could not
	// read; never null.
	Problems []string `json:"problems"`
}

// PlanApproveResult is what Deps.ApprovePlan reports: one of the PlanApprove
// outcomes and the whole result in plain words (what the daemon does next,
// and any problem with the push), which the page shows uncut.
type PlanApproveResult struct {
	Outcome string `json:"outcome"`
	Text    string `json:"text"`
}

// The outcomes a PlanApproveResult takes.
const (
	// PlanApproved: the plan was approved now.
	PlanApproved = "approved"
	// PlanApproveAlready: the plan was approved before; nothing was done.
	PlanApproveAlready = "already"
	// PlanApproveRefused: nothing was done, because the plan cannot be
	// approved as it stands (wrong phase, checker errors, no project).
	PlanApproveRefused = "refused"
	// PlanApproveFailed: approving was tried and went wrong.
	PlanApproveFailed = "failed"
)

// planApproveWriteLimit is how long the approve POST's answer may take to
// write. The server's write timeout is 30 seconds, but the push inside
// approve may take up to two minutes (the root's planPatchPushTimeout), so
// without a longer deadline for this one response the owner would get a
// dropped connection after an approve that in fact went through. Three
// minutes leaves room for the rest of the approve around the push.
const planApproveWriteLimit = 3 * time.Minute

// planApproveSheetID is the id of the plan page's approve dialog, which the
// Approve the plan… link's data-confirm names.
const planApproveSheetID = "plan-approve"

// planApprove is whether the plan page offers Approve the plan…, and in one
// sentence why not when it does not.
type planApprove struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
}

// planApproveFor decides whether the plan can be approved from the page.
// Pure. seam is whether the server has Deps.ApprovePlan; planOK whether the
// plan was read. The order of the checks decides which sentence is shown when
// more than one holds, and each is a test row: the planning session's state
// comes before the plan's own problems, because a plan that is not at review
// cannot be approved however clean it is, and saying "fix 2 errors" then
// would send the owner to the wrong fix.
func planApproveFor(status PlanStatus, planOK bool, summary planlint.Summary, seam bool) planApprove {
	why := ""
	switch {
	case !seam:
		why = "Approving is not available on this page."
	case !status.Known:
		why = "The planning status could not be read."
	case status.Damaged:
		why = "The planning session's state file is damaged, so the plan cannot be approved here."
	case !status.Session:
		why = "There is no planning session, so there is nothing to approve."
	case status.Phase == plansession.PhaseApproved:
		why = "This plan is already approved."
	case status.Mode != plansession.ModeNew:
		why = "This planning session changes a plan that is already being built; its changes are in the plan, so there is nothing to approve."
	case status.Phase != plansession.PhaseReview:
		why = fmt.Sprintf("The plan can be approved once the planning session reaches review; it is at %s now.", status.Phase)
	case !status.Approvable:
		// A new session at review is approvable by definition; a status that
		// says otherwise disagrees with itself, so it is treated as unread.
		why = "The planning status could not be read."
	case !planOK:
		why = "The plan could not be read."
	case summary.Errors > 0:
		why = fmt.Sprintf("Fix %s before approving.", counted(summary.Errors, "error", "errors"))
	}
	return planApprove{Available: why == "", Reason: why}
}

// counted is "1 task" or "3 tasks". (summary.go's plural is the "ago" form.)
func counted(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// planBase is a project's plan view's address.
func planBase(project string) string {
	return "/p/" + url.PathEscape(project) + "/plan"
}

// planApprovePath is the approve POST's address, and also the no-JS confirm
// page's (that one is a GET).
func planApprovePath(project string) string {
	return planBase(project) + "/approve"
}

// planApproveSheetFor builds the approve confirm, which the dialog on the
// plan page and the no-JS confirm page both draw. Pure. It names the project,
// the plan's path and the counts, so the owner sees what they are starting
// before they start it. It does not name the first task: which task runs
// first is the daemon's choice when it next looks, not the page's.
func planApproveSheetFor(project, planPath string, s planlint.Summary) ConfirmSheet {
	body := fmt.Sprintf("afk starts building %s in %s from %s. The checker found %s and %s.",
		counted(s.Tasks, "task", "tasks"), counted(s.Milestones, "milestone", "milestones"), planPath,
		counted(s.Errors, "error", "errors"), counted(s.Warnings, "warning", "warnings"))
	return ConfirmSheet{
		ID:    planApproveSheetID,
		Title: "Approve the plan for " + project + "?",
		Body:  body, Action: "Approve and start", Loading: "Approving…",
		Method: "post", Path: planApprovePath(project), Safe: "Not yet",
	}
}

// ApproveSheet is the plan's approve confirm, for the plan page's dialog.
// It is a method so the template draws the same sheet the no-JS page does.
func (v PlanView) ApproveSheet() ConfirmSheet {
	return planApproveSheetFor(v.Project, v.Path, v.Summary)
}

// planStatusFor asks the seam for the project's planning status, or says it
// is unknown when there is none. Problems is made an empty list so the JSON
// never carries null.
func (s *server) planStatusFor(root string) PlanStatus {
	st := PlanStatus{}
	if s.d.PlanStatus != nil {
		st = s.d.PlanStatus(root)
	}
	if st.Problems == nil {
		st.Problems = []string{}
	}
	return st
}

// PlanConfirmView is the no-JS confirm page of Approve the plan…. It has no
// JSON form. Available is false when the plan cannot be approved, and Reason
// then says why in the plan page's words.
type PlanConfirmView struct {
	Project   string
	Sheet     ConfirmSheet
	Available bool
	Reason    string
	// Back is the plan view's address, where the safe link goes.
	Back string
}

// PlanApproveView is the API's answer to an approve: the project and what
// happened, for every outcome.
type PlanApproveView struct {
	Project string            `json:"project"`
	Result  PlanApproveResult `json:"result"`
}

// PlanResultView is the page that shows an approve's result. It has no JSON
// form (the API answers PlanApproveView).
type PlanResultView struct {
	Project string
	// Title is the page's heading, from the outcome.
	Title  string
	Result PlanApproveResult
	// Back is the plan view's address.
	Back string
}

// planResultTitles are the result page's headings by outcome.
var planResultTitles = map[string]string{
	PlanApproved:       "Plan approved",
	PlanApproveAlready: "Already approved",
	PlanApproveRefused: "Not approved",
	PlanApproveFailed:  "Approval did not finish",
}

// planApproveAnswer is the pure part of an approve's answer: the status for
// the result and the result as it is shown. An outcome this page does not
// know is a failure, so it is never shown as a success; an empty text gets a
// plain sentence, so the page is never blank.
func planApproveAnswer(res PlanApproveResult) (int, PlanApproveResult) {
	status := http.StatusOK
	switch res.Outcome {
	case PlanApproved, PlanApproveAlready:
	case PlanApproveRefused:
		status = http.StatusBadRequest
	case PlanApproveFailed:
		status = http.StatusInternalServerError
	default:
		status = http.StatusInternalServerError
		res = PlanApproveResult{Outcome: PlanApproveFailed, Text: "Approving gave an answer this page does not know. This is a fault in afk; please report it. Run afk plan status in the project folder to see where the plan is now."}
	}
	if res.Text == "" {
		switch res.Outcome {
		case PlanApproved:
			res.Text = "The plan is approved. afk starts building it."
		case PlanApproveAlready:
			res.Text = "This plan was already approved. Nothing changed."
		default:
			res.Text = "The plan was not approved, and no reason was given. Run afk plan status in the project folder to see where the plan is now."
		}
	}
	return status, res
}

// planApprove is the approve POST: what `afk plan approve` does, through
// Deps.ApprovePlan, with the project's own root. Nothing reaches the seam
// without the confirm in the body.
func (s *server) planApprove(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.findPlanProject(w, r)
	if !ok {
		return
	}
	if s.d.ApprovePlan == nil {
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
		s.fail(w, r, http.StatusBadRequest, "Nothing changed: approving the plan needs the confirm.")
		return
	}
	// ErrNotSupported (a writer with no connection behind it, as in a test)
	// means there is no deadline to move; anything else is ignored too, as
	// live.go does: the worst case is the 30-second limit we started with.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(planApproveWriteLimit))
	status, res := planApproveAnswer(s.d.ApprovePlan(p.Root))
	if isAPI(r.URL.Path) {
		writeJSON(w, status, PlanApproveView{Project: p.Name, Result: res})
		return
	}
	// A result page and not a redirect with a toast: the text says what the
	// daemon does next and any trouble with the push, which a fixed toast
	// cannot carry and would cut.
	s.renderPage(w, r, status, "plan_result", PlanResultView{
		Project: p.Name, Title: planResultTitles[res.Outcome], Result: res, Back: planBase(p.Name),
	})
}

// planApprovePage is the no-JS page behind Approve the plan…. It is not in
// routes: it has no JSON form, so it must not get an /api twin. It asks the
// sheet's question when the plan can be approved and otherwise says why, in
// the plan page's words.
func (s *server) planApprovePage(w http.ResponseWriter, r *http.Request) {
	p, waiting, ok := s.findPlanProject(w, r)
	if !ok {
		return
	}
	v := s.planView(p, waiting)
	cv := PlanConfirmView{Project: p.Name, Sheet: v.ApproveSheet(), Available: v.Approve.Available, Reason: v.Approve.Reason, Back: planBase(p.Name)}
	status := http.StatusOK
	if !cv.Available {
		status = http.StatusBadRequest
	}
	s.renderPage(w, r, status, "plan_confirm", cv)
}
