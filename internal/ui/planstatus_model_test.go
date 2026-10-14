package ui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/dphaener/afk/internal/project"
	"github.com/dphaener/afk/internal/session"
)

// extendRounds is an extend session with one unanswered question.
func extendRounds() PlanStatus {
	return PlanStatus{Known: true, Session: true, Mode: "extend", Phase: "rounds", OpenQuestions: 1, Questions: 1, PlanExists: true}
}

// TestPlanStatusCmdNil pins that an unwired seam adds no command, so a
// dashboard with no plan status behaves as it did before this existed.
func TestPlanStatusCmdNil(t *testing.T) {
	m := testModel()
	if m.deps.LoadPlanStatus != nil {
		t.Fatal("precondition: testDeps must leave LoadPlanStatus nil")
	}
	if m.planStatusCmd() != nil {
		t.Fatal("planStatusCmd() returned a command with no seam wired")
	}
	if got := len(m.projectCmds()); got != 3 {
		t.Fatalf("projectCmds() has %d commands with no seam, want the usual 3", got)
	}
	if d := (Deps{}).withDefaults(); d.LoadPlanStatus != nil {
		t.Error("withDefaults set LoadPlanStatus; it reads real files and must stay opt-in")
	}
}

// TestPlanStatusCmdCallsSeamOnce pins that the seam runs only inside the
// command, once, with the active repo, and that Known is set on landing.
func TestPlanStatusCmdCallsSeamOnce(t *testing.T) {
	calls := 0
	var gotRepo string
	d := testDeps()
	d.LoadPlanStatus = func(repo string) PlanStatus {
		calls++
		gotRepo = repo
		return extendRounds()
	}
	m := New("/alpha", time.Second, d)
	m.ready, m.width, m.height = true, 120, 40
	m.activeProject = 0
	m.deps.Projects = project.ProjectsFromPaths([]string{"/alpha"})
	cmd := m.planStatusCmd()
	if cmd == nil {
		t.Fatal("planStatusCmd() is nil with a seam wired")
	}
	if calls != 0 {
		t.Fatal("the seam ran before the command did")
	}
	msg, ok := cmd().(planStatusLoadedMsg)
	if !ok || calls != 1 || gotRepo != "/alpha" {
		t.Fatalf("msg %#v, calls %d, repo %q", msg, calls, gotRepo)
	}
	m, _ = update(t, m, msg)
	if !m.planStatus.Known || m.planStatus.Mode != "extend" {
		t.Errorf("status not stored with Known set: %#v", m.planStatus)
	}
}

// TestPlanStatusLoadedStale is the row a project switch depends on: a read
// cannot be cancelled, so an old generation must be dropped, and a switch
// must clear what is held.
func TestPlanStatusLoadedStale(t *testing.T) {
	m := testModel()
	m, _ = update(t, m, planStatusLoadedMsg{status: extendRounds(), gen: m.projectGen - 1})
	if m.planStatus.Known {
		t.Error("a stale-generation read was stored")
	}
	m, _ = update(t, m, planStatusLoadedMsg{status: extendRounds(), gen: m.projectGen})
	if !m.planStatus.Known {
		t.Fatal("a current read was dropped")
	}
	m.forgetPlanStatus()
	if m.planStatus.Known {
		t.Error("forgetPlanStatus kept the status")
	}
}

// TestPlanStatusOverviewLine: only master-plan's overview shows the line.
func TestPlanStatusOverviewLine(t *testing.T) {
	for _, tc := range []struct {
		name   string
		sess   string
		status PlanStatus
		want   bool
	}{
		{"master-plan shows it", session.PlanSessionName, extendRounds(), true},
		{"another session does not", "feature-x", extendRounds(), false},
		{"unknown status shows nothing", session.PlanSessionName, PlanStatus{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, w := range []int{40, 120} {
				lines := overviewPanel{}.Render(panelContext{Session: sess(tc.sess), PlanStatus: tc.status}, w)
				text := xansi.Strip(strings.Join(lines, "\n"))
				if got := strings.Contains(text, "plan: extend · rounds"); got != tc.want {
					t.Errorf("width %d: line present = %v, want %v\n%s", w, got, tc.want, text)
				}
				for _, ln := range lines {
					if lipgloss.Width(ln) > w {
						t.Errorf("width %d: line over the pane: %q", w, ln)
					}
				}
			}
		})
	}
}

// TestPlanStatusBuildTabLine: the Build tab shows the line for a session and
// leaves it out with none, and never overflows the pane.
func TestPlanStatusBuildTabLine(t *testing.T) {
	review := PlanStatus{Known: true, Session: true, Mode: "new", Phase: "review", PlanExists: true}
	for _, tc := range []struct {
		name   string
		status PlanStatus
		want   string
	}{
		{"extend at rounds", extendRounds(), "plan: extend · rounds · 1 question has no answer yet"},
		{"new plan at review", review, "plan: plan waiting for your approval"},
		{"load error", PlanStatus{Known: true, Err: "bad afk.toml"}, "plan: plan status unreadable: bad afk.toml"},
		{"no session", PlanStatus{Known: true}, ""},
		{"not known yet", PlanStatus{Session: true, Mode: "new", Phase: "review"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, w := range []int{40, 120} {
				lines := plain(buildTabLines(buildSnap("/alpha"), tc.status, w))
				text := strings.Join(lines, "\n")
				if tc.want == "" {
					if strings.Contains(text, "plan:") {
						t.Errorf("width %d: unexpected plan line:\n%s", w, text)
					}
				} else if w == 120 && !strings.Contains(text, tc.want) {
					t.Errorf("missing %q in:\n%s", tc.want, text)
				}
				if tc.want != "" && !strings.HasPrefix(lines[1], "plan:") {
					t.Errorf("width %d: plan line is not second: %q", w, lines)
				}
				for _, ln := range lines {
					if lipgloss.Width(ln) > w {
						t.Errorf("width %d: line over the pane: %q", w, ln)
					}
				}
			}
		})
	}
}
