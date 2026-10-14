package main

import (
	"testing"

	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/session"
	"github.com/dphaener/afk/internal/usagestate"
	"github.com/dphaener/afk/internal/web"
)

// TestWebSessionCleanOutcome covers the mapping from a clean's result to the
// page's. Load-bearing rows: the phase-gate refusal must not tell a phone
// user to "pass --force" (the page cannot) and must name where to force; the
// three success sentences must keep the dashboard's words and say the branch
// was kept; a failure carries what the teardown wrote on stderr.
func TestWebSessionCleanOutcome(t *testing.T) {
	gate := `session "s" is in phase "planning" (not done/failed)` + forceHint
	tests := []struct {
		name string
		res  cleanSurfaceResult
		want web.SessionActionResult
	}{
		{"archived", cleanSurfaceResult{Outcome: cleanCleaned, Result: session.CleanResult{Name: "s", Branch: "afk/s", ArchivePath: "/x/archive/s-20260101"}},
			web.SessionActionResult{Outcome: "done", Text: "Cleaned s; archived as s-20260101. Branch afk/s kept."}},
		{"one-off removed", cleanSurfaceResult{Outcome: cleanCleaned, Result: session.CleanResult{Name: "s"}},
			web.SessionActionResult{Outcome: "done", Text: "Cleaned s; session directory removed."}},
		{"worktree fault", cleanSurfaceResult{Outcome: cleanCleaned, Result: session.CleanResult{Name: "s", ArchiveSkipped: true}},
			web.SessionActionResult{Outcome: "done", Text: "Cleaned s; worktree removed, nothing to archive."}},
		{"snapshot noted", cleanSurfaceResult{Outcome: cleanCleaned, Result: session.CleanResult{Name: "s", ArchivePath: "/a/b", SnapshotCommitted: true}},
			web.SessionActionResult{Outcome: "done", Text: "Cleaned s; archived as b. Dirty changes were snapshot-committed."}},
		{"phase gate", cleanSurfaceResult{Outcome: cleanRefusedOutcome, Text: gate},
			web.SessionActionResult{Outcome: "refused", Text: `session "s" is in phase "planning" (not done/failed). This page does not force a clean. Run ` + "`afk clean --force s`" + ` or use ` + "`afk dash`" + ` if you want it cleaned anyway.`}},
		{"changed since shown", cleanSurfaceResult{Outcome: cleanRefusedOutcome, Text: "session changed"},
			web.SessionActionResult{Outcome: "refused", Text: "session changed"}},
		{"not found", cleanSurfaceResult{Outcome: cleanNotFound, Text: "no session named"},
			web.SessionActionResult{Outcome: "refused", Text: "no session named"}},
		{"failed", cleanSurfaceResult{Outcome: cleanFailed, Text: "boom", EchoStderr: "warn a\nwarn b\n"},
			web.SessionActionResult{Outcome: "failed", Text: "boom (warn a · warn b)"}},
		{"failed quiet", cleanSurfaceResult{Outcome: cleanFailed, Text: "boom"},
			web.SessionActionResult{Outcome: "failed", Text: "boom"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := webCleanOutcome(tt.res, "s"); got != tt.want {
				t.Errorf("got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

// TestWebSessionActionResult: the three known outcomes pass through; an
// unknown one is a failure, never passed on to the page.
func TestWebSessionActionResult(t *testing.T) {
	for _, o := range []string{"done", "refused", "failed"} {
		t.Run(o, func(t *testing.T) {
			got := webActionResult(sessionActionResult{Outcome: o, Text: "t"})
			if got.Outcome != o || got.Text != "t" {
				t.Errorf("got %+v", got)
			}
		})
	}
	t.Run("unknown", func(t *testing.T) {
		if got := webActionResult(sessionActionResult{Outcome: "weird"}); got.Outcome != web.SessionActionFailed {
			t.Errorf("got %+v", got)
		}
	})
}

// TestDaemonWebSessionSeams: the daemon's deps offer all four session
// actions, and a stranded row is marked so the page words its clean right.
func TestDaemonWebSessionSeams(t *testing.T) {
	d := daemonWebDeps(nil, t.TempDir(), usagestate.Deps{}, nil)
	if d.CleanSession == nil || d.RestartSession == nil || d.SetSessionLabel == nil || d.NewSession == nil {
		t.Fatalf("a session seam is nil: %+v", d)
	}
}

func TestWebSessionViewsStranded(t *testing.T) {
	got := webSessionViews([]discover.SessionState{
		{Name: "a", Kind: session.KindStatusWithoutWorktree},
		{Name: "b", Kind: session.KindHealthy},
	})
	if !got[0].Stranded || got[1].Stranded {
		t.Errorf("Stranded = %v, %v; want true, false", got[0].Stranded, got[1].Stranded)
	}
}
