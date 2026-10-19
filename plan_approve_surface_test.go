package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/planlint"
	"github.com/dphaener/afk/internal/plansession"
	"github.com/dphaener/afk/internal/session"
)

// surface runs the entry point on the rig, as the dashboard would: the
// project's root as the folder and no flags. commitFails follows failStep, as
// in approve, so the crash rows have one switch.
func (r *approveRig) surface(tweak func(*approveDeps)) approveResult {
	r.t.Helper()
	r.git.commitFails = r.failStep == approveCommit
	deps := r.deps()
	if tweak != nil {
		tweak(&deps)
	}
	return approvePlanForSurface(r.root, deps)
}

// stdoutOf and stderrOf join a result's lines for one stream, as the command
// prints them.
func stdoutOf(res approveResult) string { return linesOf(res, false) }
func stderrOf(res approveResult) string { return linesOf(res, true) }

func linesOf(res approveResult, toStderr bool) string {
	var b strings.Builder
	for _, l := range res.Lines {
		if l.toStderr == toStderr {
			b.WriteString(l.text + "\n")
		}
	}
	return b.String()
}

// TestApprovePlanForSurface pins the dashboard's entry point: the outcome,
// exit code and structured fields of every way a run can end before or
// after the steps, and that a refusal changes nothing.
//
// Load-bearing rows:
//   - "approved": every structured field the dashboard words a notice from
//     (plan path, the checker's counts, the daemon, the push) is filled, and
//     every line is for stdout, as the command prints a clean run.
//   - "no project, folder missing" and "no project, cannot tell": the code is
//     findPlanProject's own (1 and 2), not a flat 1, so the command keeps its
//     exit codes.
//   - every refusal row: Code 1, the reason without "afk plan approve: " in
//     front, both refusal lines on stderr, and git asked nothing but status,
//     because the gate changes nothing.
//   - the push rows: a commit that could not be pushed still ends approved,
//     and the push outcome is what the dashboard says about it.
//
// No row reaches "the plan is outside the project": projectconfig refuses a
// plan path that is absolute or climbs out with "..", and afk.toml is the
// only place the path comes from. The "unreadable afk.toml" row is that
// refusal as the owner meets it.
func TestApprovePlanForSurface(t *testing.T) {
	review := func(r *approveRig) { r.seed(plansession.ModeNew, plansession.PhaseReview) }
	tests := []struct {
		name  string
		setup func(r *approveRig)
		// tweak changes the deps after the rig made them; folder replaces the
		// folder passed in ("" keeps the project root, "-" passes "").
		tweak       func(*approveDeps)
		folder      string
		wantOutcome approveOutcome
		wantCode    int
		wantReason  []string
		wantPush    approvePushOutcome
		// wantStderr is a part of what goes to stderr ("" means nothing does).
		wantStderr string
	}{
		{
			name:        "approved",
			setup:       func(r *approveRig) { review(r); r.daemon = true },
			wantOutcome: approveApproved,
			wantPush:    approvePushed,
		},
		{
			name:        "no project, folder missing",
			setup:       func(r *approveRig) {},
			folder:      "/nonexistent-t78",
			wantOutcome: approveNoProject,
			wantCode:    1,
			wantReason:  []string{"the folder /nonexistent-t78 was not found.", "mkdir -p /nonexistent-t78"},
			wantStderr:  "afk plan approve: the folder /nonexistent-t78 was not found.",
		},
		{
			name:        "no project, cannot tell",
			setup:       func(r *approveRig) {},
			tweak:       func(d *approveDeps) { d.plan.cwd = "" },
			folder:      "-",
			wantOutcome: approveNoProject,
			wantCode:    2,
			wantReason:  []string{"cannot tell which folder to use"},
			wantStderr:  "afk plan approve: cannot tell which folder to use",
		},
		{
			name:        "no session",
			setup:       func(r *approveRig) {},
			wantOutcome: approveRefused, wantCode: 1,
			wantReason: []string{"There is no planning session here"},
		},
		{
			name: "damaged state",
			setup: func(r *approveRig) {
				writePlanFile(r.t, r.root, ".afk/plan/state.json", "{not json")
			},
			wantOutcome: approveRefused, wantCode: 1,
			wantReason: []string{"state.json"},
		},
		{
			name:        "change session",
			setup:       func(r *approveRig) { r.seed(plansession.ModeAmend, plansession.PhaseBrief) },
			wantOutcome: approveRefused, wantCode: 1,
			wantReason: []string{"changes a plan that already exists"},
		},
		{
			name:        "wrong phase",
			setup:       func(r *approveRig) { r.seed(plansession.ModeNew, plansession.PhaseCheck) },
			wantOutcome: approveRefused, wantCode: 1,
			wantReason: []string{"the session is at the " + plansession.PhaseCheck + " phase, not review"},
		},
		{
			name:        "checker errors",
			setup:       func(r *approveRig) { review(r); r.plan.lint = 2 },
			wantOutcome: approveRefused, wantCode: 1,
			wantReason: []string{"2 errors", "afk plan lint"},
		},
		{
			name: "the daemon would refuse the project",
			setup: func(r *approveRig) {
				review(r)
				writePlanFile(r.t, r.root, ".afk/build/ledger.json", "{not json")
			},
			wantOutcome: approveRefused, wantCode: 1,
			wantReason: []string{"The daemon could not build from this project", "ledger.json"},
		},
		{
			name:        "not a git repository",
			setup:       func(r *approveRig) { review(r); r.plan.commonDir = "" },
			wantOutcome: approveRefused, wantCode: 1,
			wantReason: []string{"is not a git repository", "git init"},
		},
		{
			name: "unreadable afk.toml",
			setup: func(r *approveRig) {
				review(r)
				writePlanFile(r.t, r.root, "afk.toml", "plan = \"../elsewhere/PLAN.md\"\n")
			},
			wantOutcome: approveRefused, wantCode: 1,
			wantReason: []string{"afk.toml"},
		},
		{
			name: "unreadable machine config",
			setup: func(r *approveRig) {
				review(r)
				r.cfg.text, r.cfg.exists = "[[project\n", true
			},
			wantOutcome: approveRefused, wantCode: 1,
			wantReason: []string{"afk's config cannot be read"},
		},
		{
			name: "git cannot answer",
			setup: func(r *approveRig) {
				review(r)
				writePlanFile(r.t, r.root, "afk.toml", "plan = \"docs/PLAN.md\"\n")
				r.git.statusFails = true
			},
			wantOutcome: approveRefused, wantCode: 1,
			wantReason: []string{"git could not say whether the plan is committed"},
		},
		{
			name:        "push: no origin",
			setup:       func(r *approveRig) { review(r); r.git.origin = false },
			wantOutcome: approveApproved,
			wantPush:    approvePushNoOrigin,
		},
		{
			name: "push: via_pr",
			setup: func(r *approveRig) {
				review(r)
				writePlanFile(r.t, r.root, "afk.toml", "plan = \"docs/PLAN.md\"\n\n[planning]\nvia_pr = true\n")
			},
			wantOutcome: approveApproved,
			wantPush:    approvePushViaPR,
		},
		{
			name:        "push: not on a branch",
			setup:       func(r *approveRig) { review(r); r.git.detached = true },
			wantOutcome: approveApproved,
			wantPush:    approvePushNoBranch,
			wantStderr:  "Note: the commit was not pushed, because the project is not on a branch.",
		},
		{
			name:        "push: fails",
			setup:       func(r *approveRig) { review(r); r.git.pushFails = true },
			wantOutcome: approveApproved,
			wantPush:    approvePushFailed,
			wantStderr:  "Note: the commit could not be pushed",
		},
		{
			name: "push: documents already committed",
			setup: func(r *approveRig) {
				review(r)
				writePlanFile(r.t, r.root, "afk.toml", "plan = \"docs/PLAN.md\"\n")
				r.git.dirty = false
			},
			wantOutcome: approveApproved,
			wantPush:    approvePushNotAttempted,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newApproveRig(t)
			tt.setup(r)
			before := folderEntries(t, r.root)
			stateBefore, _ := os.ReadFile(plansession.StatePath(r.root))
			cfgBefore := r.cfg.text

			folder := r.root
			switch tt.folder {
			case "":
			case "-":
				folder = ""
			default:
				folder = tt.folder
			}
			deps := r.deps()
			if tt.tweak != nil {
				tt.tweak(&deps)
			}
			res := approvePlanForSurface(folder, deps)

			if res.Outcome != tt.wantOutcome || res.Code != tt.wantCode {
				t.Fatalf("outcome %q code %d, want %q code %d; lines %+v", res.Outcome, res.Code, tt.wantOutcome, tt.wantCode, res.Lines)
			}
			for _, want := range tt.wantReason {
				if !strings.Contains(res.Reason, want) {
					t.Errorf("reason %q does not contain %q", res.Reason, want)
				}
			}
			if strings.HasPrefix(res.Reason, planApproveCmd) {
				t.Errorf("reason starts with the command's name: %q", res.Reason)
			}
			errOut := stderrOf(res)
			if tt.wantStderr == "" && tt.wantOutcome == approveApproved && errOut != "" {
				t.Errorf("stderr lines %q, want none", errOut)
			}
			if tt.wantStderr != "" && !strings.Contains(errOut, tt.wantStderr) {
				t.Errorf("stderr lines %q, want %q in them", errOut, tt.wantStderr)
			}

			switch tt.wantOutcome {
			case approveNoProject:
				if res.Root != "" || len(res.Lines) != 1 || res.Lines[0].text != planApproveCmd+": "+res.Reason {
					t.Errorf("no project: root %q, lines %+v", res.Root, res.Lines)
				}
			case approveRefused:
				want := []approveLine{
					{toStderr: true, text: planApproveCmd + ": the plan was not approved.\n" + res.Reason},
					{toStderr: true, text: planNothingChanged},
				}
				if !reflect.DeepEqual(res.Lines, want) {
					t.Errorf("refusal lines = %+v, want %+v", res.Lines, want)
				}
				if res.Root != r.root || res.Done != nil || res.Left != nil || res.Push.Outcome != approvePushNotAttempted || res.Counts.Read {
					t.Errorf("refusal carries a run: %+v", res)
				}
				for _, v := range r.git.verbs() {
					if v != "status" {
						t.Errorf("a refusal ran git %s", v)
					}
				}
				if after := folderEntries(t, r.root); !reflect.DeepEqual(before, after) {
					t.Errorf("a refusal changed the project folder:\nbefore %v\nafter  %v", before, after)
				}
				if stateAfter, _ := os.ReadFile(plansession.StatePath(r.root)); !bytes.Equal(stateBefore, stateAfter) {
					t.Errorf("a refusal changed state.json")
				}
				if r.cfg.writes != 0 || r.cfg.text != cfgBefore {
					t.Errorf("a refusal wrote the machine config")
				}
			case approveApproved:
				if res.Root != r.root || res.PlanRel != "docs/PLAN.md" || res.Reason != "" || res.Err != nil {
					t.Errorf("approved: root %q plan %q reason %q err %v", res.Root, res.PlanRel, res.Reason, res.Err)
				}
				if res.Push.Outcome != tt.wantPush {
					t.Errorf("push = %+v, want %q", res.Push, tt.wantPush)
				}
				switch res.Push.Outcome {
				case approvePushed:
					if res.Push.Branch != "main" || res.Push.Err != nil {
						t.Errorf("push = %+v, want main and no error", res.Push)
					}
				case approvePushFailed:
					if res.Push.Branch != "main" || res.Push.Err == nil || !strings.Contains(res.Push.Err.Error(), "permission denied") {
						t.Errorf("push = %+v, want main and git's error", res.Push)
					}
				default:
					if res.Push.Branch != "" || res.Push.Err != nil {
						t.Errorf("push = %+v, want no branch and no error", res.Push)
					}
				}
				if want := planlint.Lint([]byte(daemonTestPlan)).Summary; !res.Counts.Read || res.Counts.Summary != want {
					t.Errorf("counts = %+v, want the checker's %+v", res.Counts, want)
				}
				if res.DaemonRunning != r.daemon {
					t.Errorf("daemon running = %v, want %v", res.DaemonRunning, r.daemon)
				}
				if len(res.Left) != 0 || len(res.Done) == 0 || res.Done[len(res.Done)-1] != approvePhase {
					t.Errorf("done %v left %v, want the phase last and nothing left", res.Done, res.Left)
				}
				if got := r.phase(); got != plansession.PhaseApproved {
					t.Errorf("phase = %q, want approved", got)
				}
			}
		})
	}
}

// TestApprovePlanForSurfaceFailures fails each step in turn and checks what
// the result says about the run.
//
// Every row is load-bearing: Failed and Err name the step and why, Done is
// the steps before it and Left the failed one and the rest (the dashboard's
// "done: ...; not done: ..." comes from these), the code is 1, the phase is
// still review, and every line is on stderr after the steps that worked. The
// push is "pushed" exactly when the commit was made before the failure,
// which is the fact the dashboard must not leave out.
func TestApprovePlanForSurfaceFailures(t *testing.T) {
	all := []approveStep{approveWriteConfig, approveCommit, approveLedger, approveProject, approvePhase}
	for i, failed := range all {
		t.Run(failed.todo(), func(t *testing.T) {
			r := newApproveRig(t)
			r.seed(plansession.ModeNew, plansession.PhaseReview)
			r.failStep = failed

			res := r.surface(nil)
			if res.Outcome != approveFailed || res.Code != 1 {
				t.Fatalf("outcome %q code %d, want failed 1; lines %+v", res.Outcome, res.Code, res.Lines)
			}
			if res.Failed != failed || res.Err == nil {
				t.Errorf("failed %v err %v, want %v and an error", res.Failed, res.Err, failed)
			}
			if len(res.Done) != i || (i > 0 && !reflect.DeepEqual(res.Done, all[:i])) {
				t.Errorf("done = %v, want %v", res.Done, all[:i])
			}
			if !reflect.DeepEqual(res.Left, all[i:]) {
				t.Errorf("left = %v, want %v", res.Left, all[i:])
			}
			wantPush := approvePushNotAttempted
			if failed > approveCommit {
				wantPush = approvePushed
			}
			if res.Push.Outcome != wantPush {
				t.Errorf("push = %q, want %q", res.Push.Outcome, wantPush)
			}
			if res.Reason != "" || res.Counts.Read || res.DaemonRunning {
				t.Errorf("a failed run carries a refusal or a closing: %+v", res)
			}
			if got := r.phase(); got != plansession.PhaseReview {
				t.Errorf("phase = %q, want review", got)
			}
			// The four report lines close the run, all on stderr.
			n := len(res.Lines)
			if n < 4 {
				t.Fatalf("lines = %+v, want the four report lines", res.Lines)
			}
			for _, l := range res.Lines[n-4:] {
				if !l.toStderr {
					t.Errorf("report line %q is not on stderr", l.text)
				}
			}
			if want := planApproveCmd + ": could not " + failed.todo() + ".\n" + res.Err.Error(); res.Lines[n-4].text != want {
				t.Errorf("first report line = %q, want %q", res.Lines[n-4].text, want)
			}
		})
	}
}

// TestApprovePlanForSurfaceSecondRun is a full run and then the same call
// again. The second is the load-bearing half: already approved, code 0, one
// stdout line, and nothing touched (git, the machine config, the project
// folder, state.json).
func TestApprovePlanForSurfaceSecondRun(t *testing.T) {
	r := newApproveRig(t)
	r.seed(plansession.ModeNew, plansession.PhaseReview)
	if res := r.surface(nil); res.Outcome != approveApproved {
		t.Fatalf("first run: %q; lines %+v", res.Outcome, res.Lines)
	}
	calls, writes := len(r.git.calls), r.cfg.writes
	before := folderEntries(t, r.root)
	stateBefore := r.read(".afk/plan/state.json")

	res := r.surface(nil)
	want := []approveLine{{text: "The plan is already approved. " + planNothingChanged}}
	if res.Outcome != approveAlreadyApproved || res.Code != 0 || !reflect.DeepEqual(res.Lines, want) {
		t.Errorf("second run: outcome %q code %d lines %+v", res.Outcome, res.Code, res.Lines)
	}
	if res.Root != r.root || res.Done != nil || res.Push.Outcome != approvePushNotAttempted {
		t.Errorf("second run carries a run: %+v", res)
	}
	if len(r.git.calls) != calls || r.cfg.writes != writes {
		t.Errorf("the second run ran git or wrote the machine config")
	}
	if after := folderEntries(t, r.root); !reflect.DeepEqual(before, after) {
		t.Errorf("the second run changed the project folder")
	}
	if got := r.read(".afk/plan/state.json"); got != stateBefore {
		t.Errorf("the second run rewrote state.json")
	}
}

// TestRunPlanApprovePrintsTheSurface pins that the command is the entry point
// plus printing: on two identical rigs, what the command prints on each
// stream is the result's lines for that stream, byte for byte, once the
// rigs' temporary folders (and the project name taken from one) are made
// the same. Rows: a clean run (stdout only),
// a run with a note on stderr between stdout lines (push fails), a failure
// half way, and a refusal.
func TestRunPlanApprovePrintsTheSurface(t *testing.T) {
	tests := []struct {
		name  string
		setup func(r *approveRig)
	}{
		{"approved", func(r *approveRig) {}},
		{"push fails", func(r *approveRig) { r.git.pushFails = true }},
		{"fails at the ledger", func(r *approveRig) { r.failStep = approveLedger }},
		{"refused", func(r *approveRig) { r.plan.lint = 1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, b := newApproveRig(t), newApproveRig(t)
			for _, r := range []*approveRig{a, b} {
				r.seed(plansession.ModeNew, plansession.PhaseReview)
				tt.setup(r)
			}
			res := a.surface(nil)
			code, out, errOut := b.approve()
			same := func(s string) string {
				s = strings.ReplaceAll(s, b.root, a.root)
				// The listed name is the folder's base, which differs too.
				s = strings.ReplaceAll(s, "Added "+filepath.Base(b.root)+" ", "Added "+filepath.Base(a.root)+" ")
				return strings.ReplaceAll(s, b.stateDir, a.stateDir)
			}
			if code != res.Code {
				t.Errorf("command exit %d, entry point code %d", code, res.Code)
			}
			if got, want := same(out), stdoutOf(res); got != want {
				t.Errorf("stdout:\n got %q\nwant %q", got, want)
			}
			if got, want := same(errOut), stderrOf(res); got != want {
				t.Errorf("stderr:\n got %q\nwant %q", got, want)
			}
			if ledger.Exists(a.root) != ledger.Exists(b.root) {
				t.Errorf("the two runs left different ledgers")
			}
		})
	}
}

// TestApproveStopsThePlanningHost pins when approve ends the planning
// session's host and what it says. Load-bearing rows:
//   - "stop fails": the plan is still approved, the code is still 0, phase
//     approved is on disk, .afk/plan/ is unchanged, and one Note line on
//     stderr names the command that ends the session by hand. A failed stop
//     must never read as a failed approval.
//   - "refused" and "already approved": the stopper is never called, so an
//     approve that changed nothing leaves the agent alone.
//   - "host running" and "already gone": both say the session has ended; the
//     owner cannot tell them apart and does not need to.
func TestApproveStopsThePlanningHost(t *testing.T) {
	tests := []struct {
		name        string
		phase       string // "" seeds nothing, so the gate refuses
		stop        session.HostStop
		stopErr     error
		wantOutcome approveOutcome
		wantHost    approvePlanHostOutcome
		wantCalls   int
		wantStdout  string
		wantStderr  string
	}{
		{name: "host running and stopped", phase: plansession.PhaseReview, stop: session.HostStopped,
			wantOutcome: approveApproved, wantHost: approvePlanHostEnded, wantCalls: 1,
			wantStdout: "The planning session has ended.\n"},
		{name: "host already gone", phase: plansession.PhaseReview, stop: session.HostGone,
			wantOutcome: approveApproved, wantHost: approvePlanHostGone, wantCalls: 1,
			wantStdout: "The planning session has ended.\n"},
		{name: "stop fails", phase: plansession.PhaseReview, stopErr: errors.New("stopping host process (pid 7): did not exit\nmore."),
			wantOutcome: approveApproved, wantHost: approvePlanHostFailed, wantCalls: 1,
			wantStderr: "Note: the planning session could not be ended (stopping host process (pid 7): did not exit). End it by hand with: afk clean master-plan --project "},
		{name: "refused", phase: "", wantOutcome: approveRefused, wantCalls: 0},
		{name: "already approved", phase: plansession.PhaseApproved, wantOutcome: approveAlreadyApproved, wantCalls: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newApproveRig(t)
			if tt.phase != "" {
				r.seed(plansession.ModeNew, tt.phase)
			}
			r.stop, r.stopErr = tt.stop, tt.stopErr
			planBefore := map[string]string{}
			for _, f := range []string{"brief.md", "survey.md", "slices.md", "critic.md", "questions.jsonl"} {
				if tt.phase != "" {
					planBefore[f] = r.read(".afk/plan/" + f)
				}
			}
			res := r.surface(nil)
			if res.Outcome != tt.wantOutcome {
				t.Fatalf("outcome %q, want %q; lines %+v", res.Outcome, tt.wantOutcome, res.Lines)
			}
			if len(r.stopRoots) != tt.wantCalls {
				t.Fatalf("stopper called %d times, want %d", len(r.stopRoots), tt.wantCalls)
			}
			if tt.wantCalls == 1 && r.stopRoots[0] != r.root {
				t.Errorf("stopper called with %q, want the project root %q", r.stopRoots[0], r.root)
			}
			if res.PlanHost.Outcome != tt.wantHost {
				t.Errorf("PlanHost outcome %q, want %q", res.PlanHost.Outcome, tt.wantHost)
			}
			if tt.wantOutcome == approveApproved {
				if res.Code != 0 {
					t.Errorf("code %d, want 0", res.Code)
				}
				if got := r.phase(); got != plansession.PhaseApproved {
					t.Errorf("phase on disk %q, want approved", got)
				}
				for f, want := range planBefore {
					if got := r.read(".afk/plan/" + f); got != want {
						t.Errorf("%s changed", f)
					}
				}
			}
			if tt.wantStdout != "" && !strings.HasSuffix(stdoutOf(res), tt.wantStdout) {
				t.Errorf("stdout does not end with %q:\n%s", tt.wantStdout, stdoutOf(res))
			}
			if tt.wantStderr != "" {
				got := stderrOf(res)
				if want := tt.wantStderr + r.root + "\n"; got != want {
					t.Errorf("stderr = %q, want %q", got, want)
				}
			}
			if tt.wantHost == approvePlanHostEnded || tt.wantHost == approvePlanHostGone {
				if stderrOf(res) != "" {
					t.Errorf("unexpected stderr %q", stderrOf(res))
				}
			}
		})
	}
}
