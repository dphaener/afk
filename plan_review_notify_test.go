package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/plansession"
)

// seedReview makes a session of the given mode at the check phase whose plan
// the checker passes. afkToml, when set, is written as the project's afk.toml.
func seedReview(t *testing.T, dir, mode, afkToml string) {
	t.Helper()
	base := planFixture(t, "base-plan.md")
	seedPlanSession(t, dir, plansession.PhaseCheck, "no: a command line", base)
	if mode != plansession.ModeNew {
		writePlanFile(t, dir, ".afk/plan/state.json", `{"mode":"`+mode+`","phase":"check","updated_at":"2026-10-12T00:00:00Z"}`)
	}
	if afkToml != "" {
		writePlanFile(t, dir, "afk.toml", afkToml)
	}
}

// TestRecordPlanReady pins what `afk plan advance` records when a new plan
// reaches review: the project goes on the review list, and nothing is sent.
//
// Load-bearing rows: "new reaches review" (one review entry under the
// project's folder, nothing on stderr); "leftover ntfy_url" (a project whose
// afk.toml still names an ntfy address is listed exactly the same and nothing
// is said about it, since the plan-ready notice is gone); "state folder
// missing" (exit 0, the phase still review, one plain line on stderr);
// "change session" and "other phase" (nothing listed).
func TestRecordPlanReady(t *testing.T) {
	tests := []struct {
		name     string
		mode     string
		afkToml  string
		phase    string // phase to seed; "" means check
		stateErr error
		wantList int
		wantErr  string
	}{
		{name: "new reaches review", mode: "new", wantList: 1},
		{name: "leftover ntfy_url", mode: "new", afkToml: "[notify]\nntfy_url = \"https://ntfy.example/t\"\n", wantList: 1},
		{
			name: "state folder missing", mode: "new", stateErr: errors.New("no home folder"),
			wantErr: "afk plan advance: the plan is ready, but it could not be put on the list of plans waiting for approval: no home folder",
		},
		{name: "change session", mode: "extend"},
		{name: "amend session", mode: "amend"},
		{name: "other phase", mode: "new", phase: plansession.PhaseBrief},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			seedReview(t, dir, tt.mode, tt.afkToml)
			if tt.phase != "" {
				writePlanFile(t, dir, ".afk/plan/state.json", planState(tt.phase))
			}
			state := t.TempDir()
			f := &planFake{}
			deps := realLintDeps(f, dir)
			deps.review = planReviewDeps{stateDir: func() (string, error) { return state, tt.stateErr }}
			var so, se strings.Builder
			code := runPlanAdvance(nil, &so, &se, deps)
			reviews := daemonstate.ReadReviews(state)
			if tt.mode != plansession.ModeNew {
				// Change modes may refuse to leave check for reasons of their
				// own; what matters is that nothing was listed.
				if len(reviews) != 0 {
					t.Fatalf("a %s session added %d review entries", tt.mode, len(reviews))
				}
				return
			}
			if code != 0 {
				t.Fatalf("exit = %d\nstdout: %s\nstderr: %s", code, so.String(), se.String())
			}
			if len(reviews) != tt.wantList {
				t.Fatalf("review entries = %d, want %d", len(reviews), tt.wantList)
			}
			if tt.wantList > 0 && filepath.Clean(reviews[0].Root) != filepath.Clean(dir) {
				t.Errorf("review root = %q, want %q", reviews[0].Root, dir)
			}
			if tt.wantErr == "" && se.String() != "" {
				t.Errorf("stderr = %q, want nothing", se.String())
			}
			if tt.wantErr != "" && !strings.Contains(se.String(), tt.wantErr) {
				t.Errorf("stderr = %q, want it to contain %q", se.String(), tt.wantErr)
			}
			if tt.phase == "" {
				st, ok, err := plansession.ReadState(dir)
				if err != nil || !ok || st.Phase != plansession.PhaseReview {
					t.Errorf("state = %+v ok=%v err=%v, want phase review", st, ok, err)
				}
			}
		})
	}
}
