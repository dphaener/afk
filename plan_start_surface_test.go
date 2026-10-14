package main

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/plansession"
	"github.com/dphaener/afk/internal/session"
)

// TestStartPlanForSurface pins the dashboard's entry point: what each request
// starts, resumes or refuses, and that a refusal leaves the project byte for
// byte as it was.
//
// Load-bearing rows:
//   - "extend starts" and "amend writes the brief word for word": the same
//     seed the command writes (state.json last, the marker before the spawn),
//     and never the yes file, because the dashboard never asks for that
//     agreement.
//   - "reconcile path is the project's, not the dashboard's folder": a path
//     that exists only in the folder the dashboard runs from is not found. The
//     command would look there first; the entry point must not.
//   - "resume, host alive" (no spawn, nothing written) and "resume, host gone"
//     (one spawn on the existing state, nothing seeded): resuming never
//     replaces the open session's answers.
//   - "resume, a plan and no session" and "resume, finished session": a resume
//     request never starts a new plan and never asks which change is wanted.
//   - "open session": the refusal carries the open session's mode and phase,
//     which is what lets the dashboard offer to open it.
//   - "no plan": a mode never starts a session for a project with no plan.
//   - "reason has no command name": every refusal is the command's sentence
//     without the "afk plan <mode>: " in front.
func TestStartPlanForSurface(t *testing.T) {
	const archive = ".afk/plan/archive/20261013T090000Z/"
	plan := func(t *testing.T, dir string) { writePlanFile(t, dir, "docs/PLAN.md", "# Plan\n") }
	planAndDoc := func(t *testing.T, dir string) {
		plan(t, dir)
		writePlanFile(t, dir, "docs/design-system.md", "# Design\n")
	}
	openSession := func(t *testing.T, dir string) {
		plan(t, dir)
		writePlanFile(t, dir, ".afk/plan/state.json", planState(plansession.PhaseRounds))
		writePlanFile(t, dir, ".afk/plan/brief.md", "a brief\n")
		writePlanFile(t, dir, ".afk/plan/survey.md", "a survey\n")
	}
	stateFor := func(mode string) string {
		return "{\n  \"mode\": \"" + mode + "\",\n  \"phase\": \"brief\",\n  \"updated_at\": \"2026-10-13T09:00:00Z\"\n}\n"
	}
	tests := []struct {
		name  string
		setup func(t *testing.T, dir string)
		fake  planFake
		// req's Project may hold {dir}; "" means the project folder itself.
		req planSurfaceRequest
		// refusingAgent sets the request's provider to one that cannot plan.
		refusingAgent bool
		wantOutcome   planSurfaceOutcome
		wantReason    string
		wantOpen      bool
		wantOpenMode  string
		wantOpenPhase string
		wantArchived  string
		// wantRoot is "" for the project folder, "-" for no root at all.
		wantRoot  string
		wantCalls []string
		wantFiles map[string]string
		absent    []string
		unchanged bool
	}{
		{
			name: "extend starts", setup: plan,
			req:         planSurfaceRequest{Mode: plansession.ModeExtend},
			wantOutcome: planSurfaceStarted,
			wantCalls:   []string{"spawn marker extend/brief"},
			wantFiles:   map[string]string{".afk/plan/state.json": stateFor("extend")},
			absent:      []string{".afk/plan/brief.md", ".afk/plan/yes", ".afk/plan/archive"},
		},
		{
			name: "amend writes the brief word for word", setup: plan,
			req:         planSurfaceRequest{Mode: plansession.ModeAmend, Text: "Add a task:  export CSV, please."},
			wantOutcome: planSurfaceStarted,
			wantCalls:   []string{"spawn marker amend/brief"},
			wantFiles: map[string]string{
				".afk/plan/state.json": stateFor("amend"),
				".afk/plan/brief.md":   "Add a task:  export CSV, please.\n",
			},
			absent: []string{".afk/plan/yes"},
		},
		{
			name: "reconcile names the document in the brief", setup: planAndDoc,
			req:         planSurfaceRequest{Mode: plansession.ModeReconcile, Text: "docs/design-system.md"},
			wantOutcome: planSurfaceStarted,
			wantCalls:   []string{"spawn marker reconcile/brief"},
			wantFiles: map[string]string{
				".afk/plan/state.json": stateFor("reconcile"),
				".afk/plan/brief.md":   "Reconcile the plan with docs/design-system.md.\n",
			},
			absent: []string{".afk/plan/yes"},
		},
		{
			name: "finished session is archived", setup: planWithFinishedChange,
			req:          planSurfaceRequest{Mode: plansession.ModeAmend, Text: "a second change"},
			wantOutcome:  planSurfaceStarted,
			wantArchived: filepath.Join("{dir}", ".afk", "plan", "archive", "20261013T090000Z"),
			wantCalls:    []string{"spawn marker amend/brief"},
			wantFiles: map[string]string{
				".afk/plan/brief.md": "a second change\n",
				archive + "brief.md": "the earlier request\n",
			},
			absent: []string{".afk/plan/changes.md"},
		},
		{
			name: "a subfolder starts in the main checkout",
			setup: func(t *testing.T, dir string) {
				plan(t, dir)
				writePlanFile(t, dir, "sub/keep", "")
			},
			req:         planSurfaceRequest{Mode: plansession.ModeExtend, Project: filepath.Join("{dir}", "sub")},
			wantOutcome: planSurfaceStarted,
			wantCalls:   []string{"spawn marker extend/brief"},
			wantFiles:   map[string]string{".afk/plan/state.json": stateFor("extend")},
		},
		{
			name: "resume, host alive", setup: openSession, fake: planFake{alive: true},
			req:         planSurfaceRequest{Mode: planSurfaceResume},
			wantOutcome: planSurfaceResumed,
			unchanged:   true,
		},
		{
			name: "resume, host gone", setup: openSession,
			req:         planSurfaceRequest{Mode: planSurfaceResume},
			wantOutcome: planSurfaceResumed,
			wantCalls:   []string{"spawn marker new/rounds"},
			wantFiles: map[string]string{
				".afk/plan/state.json": planState(plansession.PhaseRounds),
				".afk/plan/brief.md":   "a brief\n",
			},
		},
		{
			name:        "resume, nothing here",
			req:         planSurfaceRequest{Mode: planSurfaceResume},
			wantOutcome: planSurfaceRefused, wantReason: "There is no planning session open in this project",
			unchanged: true,
		},
		{
			name: "resume, a plan and no session", setup: plan,
			req:         planSurfaceRequest{Mode: planSurfaceResume},
			wantOutcome: planSurfaceRefused, wantReason: "There is no planning session open in this project",
			unchanged: true,
		},
		{
			name: "resume, finished session", setup: planWithFinishedChange,
			req:         planSurfaceRequest{Mode: planSurfaceResume},
			wantOutcome: planSurfaceRefused, wantReason: "There is no planning session open in this project",
			unchanged: true,
		},
		{
			name: "resume, finished session whose agent still runs", setup: planWithFinishedChange, fake: planFake{alive: true},
			req:         planSurfaceRequest{Mode: planSurfaceResume},
			wantOutcome: planSurfaceRefused, wantReason: "There is no planning session open in this project",
			unchanged: true,
		},
		{
			name:        "no plan",
			req:         planSurfaceRequest{Mode: plansession.ModeExtend},
			wantOutcome: planSurfaceRefused, wantReason: "this project has no plan yet",
			unchanged: true,
		},
		{
			name: "open session", setup: openSession,
			req:         planSurfaceRequest{Mode: plansession.ModeAmend, Text: "something else"},
			wantOutcome: planSurfaceRefused,
			wantReason:  "a planning session is already open in this project. It is in the new mode, at the rounds phase.",
			wantOpen:    true, wantOpenMode: "new", wantOpenPhase: plansession.PhaseRounds,
			unchanged: true,
		},
		{
			name: "agent of the finished session still running", setup: planWithFinishedChange, fake: planFake{alive: true},
			req:         planSurfaceRequest{Mode: plansession.ModeExtend},
			wantOutcome: planSurfaceRefused, wantReason: "its agent is still running",
			unchanged: true,
		},
		{
			name: "the agent cannot plan", setup: plan, refusingAgent: true,
			req:         planSurfaceRequest{Mode: plansession.ModeExtend},
			wantOutcome: planSurfaceRefused, wantReason: "Nothing was started.",
			unchanged: true,
		},
		{
			name: "damaged state",
			setup: func(t *testing.T, dir string) {
				plan(t, dir)
				writePlanFile(t, dir, ".afk/plan/state.json", "{not json")
			},
			req:         planSurfaceRequest{Mode: plansession.ModeExtend},
			wantOutcome: planSurfaceRefused, wantReason: "Fix " + filepath.Join("{dir}", ".afk", "plan", "state.json") + " by hand",
			unchanged: true,
		},
		{
			name: "damaged state, resume",
			setup: func(t *testing.T, dir string) {
				plan(t, dir)
				writePlanFile(t, dir, ".afk/plan/state.json", "{not json")
			},
			req:         planSurfaceRequest{Mode: planSurfaceResume},
			wantOutcome: planSurfaceRefused, wantReason: "by hand",
			unchanged: true,
		},
		{
			name: "reconcile, missing document", setup: plan,
			req:         planSurfaceRequest{Mode: plansession.ModeReconcile, Text: "missing.md"},
			wantOutcome: planSurfaceRefused, wantReason: "the file missing.md was not found",
			unchanged: true,
		},
		{
			name: "reconcile path is the project's, not the dashboard's folder", setup: plan,
			req:         planSurfaceRequest{Mode: plansession.ModeReconcile, Text: "notes.md"},
			wantOutcome: planSurfaceRefused, wantReason: "the file notes.md was not found",
			unchanged: true,
		},
		{
			name: "reconcile, document outside the project", setup: plan,
			req:         planSurfaceRequest{Mode: plansession.ModeReconcile, Text: filepath.Join("{other}", "notes.md")},
			wantOutcome: planSurfaceRefused, wantReason: "is outside the project folder",
			unchanged: true,
		},
		{
			name: "reconcile, a folder", setup: planAndDoc,
			req:         planSurfaceRequest{Mode: plansession.ModeReconcile, Text: "docs"},
			wantOutcome: planSurfaceRefused, wantReason: "is a folder, not a file",
			unchanged: true,
		},
		{
			name: "amend with blank text", setup: plan,
			req:         planSurfaceRequest{Mode: plansession.ModeAmend, Text: "  "},
			wantOutcome: planSurfaceRefused, wantReason: "You did not say what to change.", wantRoot: "-",
			unchanged: true,
		},
		{
			name: "reconcile with no document", setup: plan,
			req:         planSurfaceRequest{Mode: plansession.ModeReconcile},
			wantOutcome: planSurfaceRefused, wantReason: "You did not name a document.", wantRoot: "-",
			unchanged: true,
		},
		{
			name: "unknown mode", setup: plan,
			req:         planSurfaceRequest{Mode: "rewrite"},
			wantOutcome: planSurfaceRefused, wantReason: `"rewrite" is not a way to change a plan`, wantRoot: "-",
			unchanged: true,
		},
		{
			name: "relative project folder", setup: plan,
			req:         planSurfaceRequest{Mode: plansession.ModeExtend, Project: "shop"},
			wantOutcome: planSurfaceRefused, wantReason: "is not a full path", wantRoot: "-",
			unchanged: true,
		},
		{
			name: "project folder missing", setup: plan,
			req:         planSurfaceRequest{Mode: plansession.ModeExtend, Project: filepath.Join("{dir}", "gone")},
			wantOutcome: planSurfaceRefused, wantReason: "the folder " + filepath.Join("{dir}", "gone") + " was not found", wantRoot: "-",
			unchanged: true,
		},
		{
			name: "bad afk.toml",
			setup: func(t *testing.T, dir string) {
				plan(t, dir)
				writePlanFile(t, dir, "afk.toml", "plan = [\n")
			},
			req:         planSurfaceRequest{Mode: plansession.ModeExtend},
			wantOutcome: planSurfaceRefused, wantReason: "afk.toml",
			unchanged: true,
		},
		{
			name: "spawn fails after the seed", setup: plan, fake: planFake{spawnErr: errors.New("no pty")},
			req:         planSurfaceRequest{Mode: plansession.ModeExtend},
			wantOutcome: planSurfaceRefused, wantReason: "could not start the planning session: no pty\nRun `afk plan` again once that is fixed",
			wantCalls: []string{"spawn marker extend/brief"},
			wantFiles: map[string]string{".afk/plan/state.json": stateFor("extend")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir, other := t.TempDir(), t.TempDir()
			writePlanFile(t, other, "notes.md", "notes\n")
			if tt.setup != nil {
				tt.setup(t, dir)
			}
			fill := func(s string) string {
				return strings.ReplaceAll(strings.ReplaceAll(s, "{dir}", dir), "{other}", other)
			}
			before := folderFiles(t, dir)
			f := tt.fake
			f.commonDir = filepath.Join(dir, ".git")
			req := tt.req
			req.Text = fill(req.Text)
			if req.Project == "" {
				req.Project = dir
			}
			req.Project = fill(req.Project)
			if tt.refusingAgent {
				req.provider = refusingProvider(t)
			}

			// The dashboard runs from some other folder; the entry point must
			// not depend on it.
			got := startPlanForSurface(req, f.deps(other))

			if got.Outcome != tt.wantOutcome {
				t.Fatalf("outcome = %q, want %q; reason: %s", got.Outcome, tt.wantOutcome, got.Reason)
			}
			if want := fill(tt.wantReason); !strings.Contains(got.Reason, want) {
				t.Errorf("reason missing %q:\n%s", want, got.Reason)
			}
			if tt.wantOutcome == planSurfaceRefused {
				if strings.HasPrefix(got.Reason, "afk ") {
					t.Errorf("reason has no command name, but starts with one: %q", got.Reason)
				}
			} else if got.Reason != "" {
				t.Errorf("reason = %q on %s, want none", got.Reason, got.Outcome)
			}
			if got.SessionOpen != tt.wantOpen || got.OpenMode != tt.wantOpenMode || got.OpenPhase != tt.wantOpenPhase {
				t.Errorf("open = %v %q %q, want %v %q %q", got.SessionOpen, got.OpenMode, got.OpenPhase, tt.wantOpen, tt.wantOpenMode, tt.wantOpenPhase)
			}
			if want := fill(tt.wantArchived); got.Archived != want {
				t.Errorf("archived = %q, want %q", got.Archived, want)
			}
			wantRoot := dir
			if tt.wantRoot == "-" {
				wantRoot = ""
			}
			if got.Root != wantRoot {
				t.Errorf("root = %q, want %q", got.Root, wantRoot)
			}
			if !reflect.DeepEqual(f.calls, tt.wantCalls) {
				t.Errorf("calls = %v, want %v", f.calls, tt.wantCalls)
			}
			for _, spec := range f.spawned {
				if spec.Name != session.PlanSessionName || !spec.MasterPlan || spec.Unattended || spec.Worktree != dir || spec.Provider != req.provider {
					t.Errorf("spawned %+v, want a planning spawn in %s with the default agent", spec, dir)
				}
			}
			if len(f.asked) != 0 {
				t.Errorf("the entry point asked a question: %q", f.asked)
			}
			after := folderFiles(t, dir)
			if tt.unchanged && !reflect.DeepEqual(before, after) {
				t.Errorf("the project was changed:\nbefore %v\nafter  %v", before, after)
			}
			for rel, want := range tt.wantFiles {
				got, ok := after[filepath.FromSlash(rel)]
				if !ok {
					t.Errorf("%s was not written; the folder holds %v", rel, folderEntries(t, dir))
				} else if got != want {
					t.Errorf("%s = %q, want %q", rel, got, want)
				}
			}
			for _, rel := range tt.absent {
				if _, ok := after[filepath.FromSlash(rel)]; ok {
					t.Errorf("%s exists and should not", rel)
				}
			}
			// No request ever writes the yes marker.
			if _, ok := after[filepath.FromSlash(".afk/plan/"+plansession.YesFile)]; ok {
				t.Errorf("the yes marker was written")
			}
		})
	}
}
