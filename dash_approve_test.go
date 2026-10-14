package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/planlint"
	"github.com/dphaener/afk/internal/ui"
)

// TestApproveResultText pins the notice for every outcome. Load-bearing rows:
// every refusal and failure ends by saying what state the plan is in
// ("Nothing was changed." or "not approved yet"), because the owner reads
// that line to decide whether to try again; "push failed" carries the exact
// command that finishes the push; the multi-line git errors keep only their
// first line, because a notice has no room for the rest.
func TestApproveResultText(t *testing.T) {
	counts := approveCounts{Read: true, Summary: planlint.Summary{Milestones: 2, Tasks: 5, OwnerTasks: 1, OpenQuestions: 3}}
	approved := func(mod func(*approveResult)) approveResult {
		r := approveResult{Outcome: approveApproved, Root: "/p", PlanRel: "docs/PLAN.md", Counts: counts}
		mod(&r)
		return r
	}
	failed := func(step approveStep, done, left []approveStep, err error) approveResult {
		return approveResult{Outcome: approveFailed, Root: "/p", Failed: step, Err: err, Done: done, Left: left}
	}
	const head = "Approved the plan at docs/PLAN.md: 5 tasks in 2 milestones, 1 task for you to do, 3 open questions."
	tests := []struct {
		name string
		res  approveResult
		want []string
		not  []string
	}{
		{"approved with daemon", approved(func(r *approveResult) { r.DaemonRunning = true; r.Push.Outcome = approvePushed }),
			[]string{head, "A running afk daemon starts the first ready task within a few seconds."}, []string{"not pushed", "No afk daemon"}},
		{"approved without daemon", approved(func(*approveResult) {}),
			[]string{head, "No afk daemon seems to be running. Start it with afk daemon, and it starts the first ready task."}, nil},
		{"approved counts unread", approved(func(r *approveResult) { r.Counts = approveCounts{} }),
			[]string{"Approved the plan at docs/PLAN.md. "}, []string{"tasks in"}},
		{"approved push failed", approved(func(r *approveResult) {
			r.Push = approvePush{Outcome: approvePushFailed, Branch: "main", Err: errors.New("\n  rejected: non-fast-forward\nhint: more")}
		}), []string{"not pushed (rejected: non-fast-forward)", "run: git -C /p push origin main"}, []string{"hint: more"}},
		{"approved no origin", approved(func(r *approveResult) { r.Push.Outcome = approvePushNoOrigin }),
			[]string{"not pushed: this repository has no origin"}, nil},
		{"approved via pr", approved(func(r *approveResult) { r.Push.Outcome = approvePushViaPR }),
			[]string{"pull requests"}, nil},
		{"approved no branch", approved(func(r *approveResult) { r.Push.Outcome = approvePushNoBranch }),
			[]string{"not on a branch"}, nil},
		{"already approved", approveResult{Outcome: approveAlreadyApproved},
			[]string{"The plan is already approved. Nothing was changed."}, nil},
		{"refused lint errors", approveResult{Outcome: approveRefused, Reason: "The plan has 2 errors"},
			[]string{"The plan has 2 errors. Nothing was changed."}, nil},
		{"refused keeps first line", approveResult{Outcome: approveRefused, Reason: "Not a repo.\nRun git init."},
			[]string{"Not a repo. Nothing was changed."}, []string{"git init"}},
		{"refused daemon", approveResult{Outcome: approveRefused, Reason: "The daemon could not build from this project as it is: x."},
			[]string{"x. Nothing was changed."}, nil},
		{"refused empty reason", approveResult{Outcome: approveRefused},
			[]string{"The plan cannot be approved. Nothing was changed."}, nil},
		{"no project", approveResult{Outcome: approveNoProject, Reason: "the folder /x was not found."},
			[]string{"the folder /x was not found. Nothing was changed."}, nil},
		{"failed at write config", failed(approveWriteConfig, nil, []approveStep{approveWriteConfig, approveCommit}, errors.New("disk full")),
			[]string{"Could not write afk.toml: disk full.", "Done: nothing;", "not done: write afk.toml; commit the plan", "The plan is not approved yet", "afk plan approve --project /p"}, nil},
		{"failed at commit", failed(approveCommit, []approveStep{approveWriteConfig}, []approveStep{approveCommit, approveLedger}, errors.New("\nfatal: unable to auto-detect email\nmore")),
			[]string{"Could not commit the plan and the project's documents: fatal: unable to auto-detect email.", "Done: wrote afk.toml;"}, []string{"more"}},
		{"failed at ledger", failed(approveLedger, []approveStep{approveWriteConfig, approveCommit}, []approveStep{approveLedger}, errors.New("e")),
			[]string{"Could not create the task ledger: e.", "Done: wrote afk.toml; committed"}, nil},
		{"failed at project", failed(approveProject, nil, []approveStep{approveProject}, errors.New("e")),
			[]string{"Could not add the project to the projects the daemon builds: e."}, nil},
		{"failed at phase", failed(approvePhase, nil, []approveStep{approvePhase}, errors.New("e")),
			[]string{"Could not mark the planning session approved: e."}, nil},
		{"failed with no error", failed(approvePhase, nil, []approveStep{approvePhase}, nil),
			[]string{"no reason was given"}, nil},
		{"unknown outcome", approveResult{Outcome: "other"},
			[]string{"Could not tell whether the plan was approved"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := approveResultText(tt.res)
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("text %q lacks %q", got, w)
				}
			}
			for _, n := range tt.not {
				if strings.Contains(got, n) {
					t.Errorf("text %q has %q", got, n)
				}
			}
			if strings.Contains(got, "\n") {
				t.Errorf("text %q runs to more than one line", got)
			}
		})
	}
}

// TestDashApprovePlanWith pins the outcome mapping and that the entry point
// is called once with the root and the given dependencies. Load-bearing row:
// "unknown" maps to failed, so a new outcome is never shown as an approval.
func TestDashApprovePlanWith(t *testing.T) {
	tests := []struct {
		name string
		in   approveOutcome
		want string
	}{
		{"approved", approveApproved, ui.PlanApproveApproved},
		{"already", approveAlreadyApproved, ui.PlanApproveAlready},
		{"refused", approveRefused, ui.PlanApproveRefused},
		{"no project", approveNoProject, ui.PlanApproveRefused},
		{"failed", approveFailed, ui.PlanApproveFailed},
		{"unknown", "other", ui.PlanApproveFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			var gotRoot string
			approve := func(root string, _ approveDeps) approveResult {
				calls++
				gotRoot = root
				return approveResult{Outcome: tt.in, Reason: "r"}
			}
			got := dashApprovePlanWith(approve, approveDeps{}, "/proj")
			if calls != 1 || gotRoot != "/proj" {
				t.Errorf("entry point called %d times with %q, want once with /proj", calls, gotRoot)
			}
			if got.Outcome != tt.want || got.Text != approveResultText(approveResult{Outcome: tt.in, Reason: "r"}) {
				t.Errorf("got %+v, want outcome %q", got, tt.want)
			}
		})
	}
}
