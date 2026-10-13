package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/planlint"
	"github.com/dphaener/afk/internal/plansession"
	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/session"
)

// planFake is a planDeps whose every outside call is recorded instead of
// made: no git, no host, no terminal. calls holds them in order.
type planFake struct {
	calls     []string
	spawned   []session.HostSpawn
	commonDir string
	tty       bool
	alive     bool
	initErr   error
	spawnErr  error
	lint      int
	// answers are what the owner types at each question, in order; asked
	// holds the questions. A question past the last answer gets "".
	answers []string
	asked   []string
}

var planFakeNow = time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)

func (f *planFake) deps(cwd string) planDeps {
	return planDeps{
		cwd:          cwd,
		gitCommonDir: func(string) string { return f.commonDir },
		gitInit: func(dir string) error {
			f.calls = append(f.calls, "init "+dir)
			return f.initErr
		},
		stdinTTY:  func() bool { return f.tty },
		stdoutTTY: func() bool { return f.tty },
		hostAlive: func(string) bool { return f.alive },
		spawn: func(spec session.HostSpawn) (session.HostProcess, error) {
			// What is on disk at the moment of the spawn is the order the
			// start promises: marker and state first, host after.
			marker := "no-marker"
			if session.IsPlanning(spec.Worktree, spec.Name) {
				marker = "marker"
			}
			state := "no-state"
			if st, ok, err := plansession.ReadState(spec.Worktree); err == nil && ok {
				state = st.Mode + "/" + st.Phase
			}
			f.calls = append(f.calls, "spawn "+marker+" "+state)
			f.spawned = append(f.spawned, spec)
			return session.HostProcess{}, f.spawnErr
		},
		attach: func(project string, started bool) (string, int, bool) {
			if started {
				f.calls = append(f.calls, "attach started")
			} else {
				f.calls = append(f.calls, "attach running")
			}
			return "left the session", 0, false
		},
		lintErrors: func([]byte) int { return f.lint },
		now:        func() time.Time { return planFakeNow },
		ask: func(prompt string) (string, error) {
			f.asked = append(f.asked, prompt)
			if len(f.asked) > len(f.answers) {
				return "", nil
			}
			return f.answers[len(f.asked)-1], nil
		},
	}
}

// writePlanFile writes one file of a project, creating its folder.
func writePlanFile(t *testing.T, project, rel, content string) {
	t.Helper()
	path := filepath.Join(project, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func planState(phase string) string {
	return `{"mode":"new","phase":"` + phase + `","updated_at":"2026-10-12T00:00:00Z"}`
}

// planQuestions is a questions.jsonl with every question answered: one
// ordinary question and the interface question with the answer given. A new
// session cannot be past the rounds phase without the second.
func planQuestions(interfaceAnswer string) string {
	return `{"id":"q1","round":1,"kind":"setup","question":"Which domain?","answer":"example.com"}` + "\n" +
		`{"id":"` + plansession.InterfaceQuestionID + `","round":1,"kind":"setup","question":"Does the project have an interface?","answer":"` + interfaceAnswer + `"}` + "\n"
}

// folderEntries lists everything under dir, so a test can say "nothing was
// written" about the whole folder and not only the files it thought of.
func folderEntries(t *testing.T, dir string) []string {
	t.Helper()
	var got []string
	err := filepath.WalkDir(dir, func(path string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != dir {
			rel, _ := filepath.Rel(dir, path)
			got = append(got, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// refusingProvider returns the name of a provider that cannot run a planning
// session, found by asking each one, so this file names no provider.
func refusingProvider(t *testing.T) string {
	t.Helper()
	for _, name := range provider.Names() {
		p, err := provider.Lookup(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.PlanArgv(); err != nil {
			return name
		}
	}
	t.Skip("every provider can run a planning session")
	return ""
}

// TestRoutePlanArgs pins which part of `afk plan` an argument list reaches.
//
// Load-bearing rows: "leading dry-run" and "leading project" (a flag first is
// the planning session, not an unknown subcommand, which is what `afk plan`
// answered before the session existed); "long help" (help is checked before
// the leading-flag rule, or --help would be handed to the session's flag
// parser); "unknown word" (a mistyped subcommand must not start a session);
// "mode word after a flag" (the mode is the first word only, so a bare
// `afk plan --project extend` stays the session with a folder named extend).
func TestRoutePlanArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want planRoute
	}{
		{"no arguments", nil, planRouteSession},
		{"leading dry-run", []string{"--dry-run"}, planRouteSession},
		{"leading project", []string{"--project", "X"}, planRouteSession},
		{"leading single dash flag", []string{"-dry-run"}, planRouteSession},
		{"long help", []string{"--help"}, planRouteHelp},
		{"short help", []string{"-h"}, planRouteHelp},
		{"help word", []string{"help"}, planRouteHelp},
		{"status", []string{"status", "--project", "X"}, planRouteStatus},
		{"advance", []string{"advance"}, planRouteAdvance},
		{"approve", []string{"approve"}, planRouteApprove},
		{"approve with a project", []string{"approve", "--project", "X"}, planRouteApprove},
		{"approve in capitals", []string{"Approve"}, planRouteUnknown},
		{"approve word after a flag", []string{"--project", "approve"}, planRouteSession},
		{"lint", []string{"lint", "docs/PLAN.md"}, planRouteLint},
		{"check", []string{"check", "s", "W1.T1", "done"}, planRouteCheck},
		{"extend", []string{"extend"}, planRouteMode},
		{"amend", []string{"amend", "add a task"}, planRouteMode},
		{"amend with a flag", []string{"amend", "--yes", "add a task"}, planRouteMode},
		{"reconcile", []string{"reconcile", "docs/design-system.md"}, planRouteMode},
		{"patch", []string{"patch", "--old", "a", "--new", "b"}, planRoutePatch},
		{"insert", []string{"insert", "--after", "T1.1", "--entry", "e"}, planRouteInsert},
		{"depend", []string{"depend", "T1.2", "T1.1"}, planRouteDepend},
		{"next-id", []string{"next-id", "--milestone", "2"}, planRouteNextID},
		{"next-id written as two words", []string{"next", "id"}, planRouteUnknown},
		{"next-followup", []string{"next-followup"}, planRouteNextFollowup},
		{"next-followup with a project", []string{"next-followup", "--project", "X"}, planRouteNextFollowup},
		{"next-followup written as one word", []string{"nextfollowup"}, planRouteUnknown},
		{"patch word after a flag", []string{"--project", "patch"}, planRouteSession},
		{"mode word after a flag", []string{"--project", "extend"}, planRouteSession},
		{"mode word in capitals", []string{"Extend"}, planRouteUnknown},
		{"unknown word", []string{"statsu"}, planRouteUnknown},
		{"empty word", []string{""}, planRouteUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := routePlanArgs(tt.args); got != tt.want {
				t.Errorf("routePlanArgs(%q) = %d, want %d", tt.args, got, tt.want)
			}
		})
	}
}

// TestDecidePlanStart is the whole decision of `afk plan`, one row per branch
// and one per ordering that matters.
//
// Load-bearing rows:
//   - "amend, session open" and its neighbours: a mode asked for while a
//     session is open is refused, in a dry run too. Without them a change
//     session would move an unfinished session's brief and answers into the
//     archive and take its place.
//   - "plan and no session, no terminal": with nobody to ask, nothing is
//     asked and nothing is started; the three commands are listed instead.
//   - "plan and no session, dry run": the dry run says the real command
//     would ask, and "extend, host alive, dry run" that a refusal comes
//     before the dry run, so a dry run never promises a start the real
//     command would refuse.
//   - "extend, no terminal": a mode start needs a terminal like any other.
//   - "amend after a finished session": the one start that seeds over an
//     existing state, and only after archiving it.
//   - "no terminal": nothing is started (not start, not attach) when nobody
//     can answer; and "no terminal, host alive" shows it beats attach too.
//   - "provider refuses, dry run" and "damaged state, dry run": the other two
//     refusals also come before the dry run.
//   - "dry run without a terminal": the dry run still answers, since it asks
//     nothing.
//   - "session at draft with a plan" and "change session open, bare": a
//     session in progress is resumed even though the plan exists, in any
//     mode; only a plan with no open session leads to the question.
//   - "new folder": the one row with both InitRepo and SeedState.
//   - "resume": a start that must not seed, or it would reset the phase.
func TestDecidePlanStart(t *testing.T) {
	refusal := errors.New("no planning with this agent")
	base := planStartInput{IsRepo: true, Terminal: true}
	with := func(edit func(*planStartInput)) planStartInput {
		in := base
		edit(&in)
		return in
	}
	// finished is a project whose last session is over and whose plan is
	// there: a change session at review, with its files still in .afk/plan/.
	finished := func(edit func(*planStartInput)) planStartInput {
		in := base
		in.HasState, in.Recorded, in.Finished = true, plansession.PhaseReview, true
		in.PlanExists, in.HasOldFiles = true, true
		if edit != nil {
			edit(&in)
		}
		return in
	}
	// open is a project with a plan and a session that is not finished.
	open := func(edit func(*planStartInput)) planStartInput {
		in := base
		in.HasState, in.Recorded = true, plansession.PhaseDraft
		in.PlanExists, in.HasOldFiles = true, true
		edit(&in)
		return in
	}
	tests := []struct {
		name string
		in   planStartInput
		want planStartAction
	}{
		{"provider refuses", with(func(in *planStartInput) { in.PlanArgvErr = refusal }),
			planStartAction{Kind: planRefuseProvider}},
		{"provider refuses, dry run", with(func(in *planStartInput) { in.PlanArgvErr = refusal; in.DryRun = true }),
			planStartAction{Kind: planRefuseProvider}},
		{"provider refuses before damaged state", with(func(in *planStartInput) { in.PlanArgvErr = refusal; in.StateDamaged = true }),
			planStartAction{Kind: planRefuseProvider}},
		{"damaged state", with(func(in *planStartInput) { in.StateDamaged = true }),
			planStartAction{Kind: planRefuseDamaged}},
		{"damaged state, dry run", with(func(in *planStartInput) { in.StateDamaged = true; in.DryRun = true }),
			planStartAction{Kind: planRefuseDamaged}},
		{"damaged state before existing plan", with(func(in *planStartInput) { in.StateDamaged = true; in.PlanExists = true }),
			planStartAction{Kind: planRefuseDamaged}},
		{"plan and no session", with(func(in *planStartInput) { in.PlanExists = true }),
			planStartAction{Kind: planChooseMode}},
		{"plan and no session, dry run", with(func(in *planStartInput) { in.PlanExists = true; in.DryRun = true }),
			planStartAction{Kind: planDryRun, AskMode: true}},
		{"plan and no session, dry run without a terminal", with(func(in *planStartInput) {
			in.PlanExists = true
			in.DryRun = true
			in.Terminal = false
		}), planStartAction{Kind: planDryRun, AskMode: true}},
		{"plan and no session, no terminal", with(func(in *planStartInput) { in.PlanExists = true; in.Terminal = false }),
			planStartAction{Kind: planRefuseNeedsMode}},
		{"plan and no session, host alive", with(func(in *planStartInput) { in.PlanExists = true; in.HostAlive = true }),
			planStartAction{Kind: planRefuseHostBusy}},
		{"session approved", with(func(in *planStartInput) {
			in.HasState = true
			in.Recorded = plansession.PhaseApproved
			in.Finished = true
			in.PlanExists = true
		}), planStartAction{Kind: planChooseMode}},
		{"change session finished, bare", finished(nil), planStartAction{Kind: planChooseMode}},
		{"change session finished, bare, no terminal", finished(func(in *planStartInput) { in.Terminal = false }),
			planStartAction{Kind: planRefuseNeedsMode}},
		{"change session finished, bare, host alive", finished(func(in *planStartInput) { in.HostAlive = true }),
			planStartAction{Kind: planRefuseHostBusy}},
		{"change session finished, bare, host alive, dry run", finished(func(in *planStartInput) { in.HostAlive = true; in.DryRun = true }),
			planStartAction{Kind: planRefuseHostBusy}},
		{"change session open, bare", with(func(in *planStartInput) {
			in.HasState = true
			in.Recorded = plansession.PhaseDraft
			in.PlanExists = true
			in.HasOldFiles = true
		}), planStartAction{Kind: planStart}},
		{"session finished and the plan gone, bare", finished(func(in *planStartInput) { in.PlanExists = false }),
			planStartAction{Kind: planRefusePlanGone}},
		{"session finished and the plan gone, dry run", finished(func(in *planStartInput) { in.PlanExists = false; in.DryRun = true }),
			planStartAction{Kind: planRefusePlanGone}},

		{"extend, plan and no session", with(func(in *planStartInput) { in.Mode = plansession.ModeExtend; in.PlanExists = true }),
			planStartAction{Kind: planStart, SeedState: true}},
		{"amend after a finished session", finished(func(in *planStartInput) { in.Mode = plansession.ModeAmend }),
			planStartAction{Kind: planStart, SeedState: true, Archive: true}},
		{"reconcile, stray files and no state", with(func(in *planStartInput) {
			in.Mode = plansession.ModeReconcile
			in.PlanExists = true
			in.HasOldFiles = true
		}), planStartAction{Kind: planStart, SeedState: true, Archive: true}},
		{"extend, folder is not a repository", planStartInput{Mode: plansession.ModeExtend, PlanExists: true, Terminal: true},
			planStartAction{Kind: planStart, InitRepo: true, SeedState: true}},
		{"extend, no plan", with(func(in *planStartInput) { in.Mode = plansession.ModeExtend }),
			planStartAction{Kind: planRefuseNoPlan}},
		{"extend, no plan, dry run", with(func(in *planStartInput) { in.Mode = plansession.ModeExtend; in.DryRun = true }),
			planStartAction{Kind: planRefuseNoPlan}},
		{"extend, no plan and a new session open", with(func(in *planStartInput) {
			in.Mode = plansession.ModeExtend
			in.HasState = true
			in.Recorded = plansession.PhaseRounds
		}), planStartAction{Kind: planRefuseNoPlan}},
		{"extend, finished session and the plan gone", finished(func(in *planStartInput) {
			in.Mode = plansession.ModeExtend
			in.PlanExists = false
		}), planStartAction{Kind: planRefuseNoPlan}},
		{"amend, session open", open(func(in *planStartInput) { in.Mode = plansession.ModeAmend }),
			planStartAction{Kind: planRefuseInProgress}},
		{"amend, session open, dry run", open(func(in *planStartInput) { in.Mode = plansession.ModeAmend; in.DryRun = true }),
			planStartAction{Kind: planRefuseInProgress}},
		{"amend, session open, host alive", open(func(in *planStartInput) { in.Mode = plansession.ModeAmend; in.HostAlive = true }),
			planStartAction{Kind: planRefuseInProgress}},
		{"amend, session open, no terminal", open(func(in *planStartInput) { in.Mode = plansession.ModeAmend; in.Terminal = false }),
			planStartAction{Kind: planRefuseInProgress}},
		{"extend, host alive", finished(func(in *planStartInput) { in.Mode = plansession.ModeExtend; in.HostAlive = true }),
			planStartAction{Kind: planRefuseHostBusy}},
		{"extend, host alive, dry run", finished(func(in *planStartInput) {
			in.Mode = plansession.ModeExtend
			in.HostAlive = true
			in.DryRun = true
		}), planStartAction{Kind: planRefuseHostBusy}},
		{"extend, no state and host alive", with(func(in *planStartInput) {
			in.Mode = plansession.ModeExtend
			in.PlanExists = true
			in.HostAlive = true
		}), planStartAction{Kind: planRefuseHostBusy}},
		{"extend, dry run", finished(func(in *planStartInput) { in.Mode = plansession.ModeExtend; in.DryRun = true }),
			planStartAction{Kind: planDryRun, SeedState: true, Archive: true}},
		{"extend, dry run without a terminal", with(func(in *planStartInput) {
			in.Mode = plansession.ModeExtend
			in.PlanExists = true
			in.DryRun = true
			in.Terminal = false
		}), planStartAction{Kind: planDryRun, SeedState: true}},
		{"extend, no terminal", finished(func(in *planStartInput) { in.Mode = plansession.ModeExtend; in.Terminal = false }),
			planStartAction{Kind: planRefuseNoTerminal}},
		{"extend, provider refuses", finished(func(in *planStartInput) { in.Mode = plansession.ModeExtend; in.PlanArgvErr = refusal }),
			planStartAction{Kind: planRefuseProvider}},
		{"extend, damaged state", with(func(in *planStartInput) {
			in.Mode = plansession.ModeExtend
			in.PlanExists = true
			in.StateDamaged = true
		}), planStartAction{Kind: planRefuseDamaged}},

		{"dry run, new folder", planStartInput{DryRun: true, Terminal: true},
			planStartAction{Kind: planDryRun, InitRepo: true, SeedState: true}},
		{"dry run without a terminal", planStartInput{DryRun: true},
			planStartAction{Kind: planDryRun, InitRepo: true, SeedState: true}},
		{"dry run, resume", with(func(in *planStartInput) {
			in.DryRun = true
			in.HasState = true
			in.Recorded = plansession.PhaseRounds
		}), planStartAction{Kind: planDryRun}},
		{"dry run, host alive", with(func(in *planStartInput) {
			in.DryRun = true
			in.HasState = true
			in.Recorded = plansession.PhaseRounds
			in.HostAlive = true
		}), planStartAction{Kind: planDryRun, Reattach: true}},
		{"no terminal", with(func(in *planStartInput) { in.Terminal = false }),
			planStartAction{Kind: planRefuseNoTerminal}},
		{"no terminal, host alive", with(func(in *planStartInput) {
			in.Terminal = false
			in.HasState = true
			in.Recorded = plansession.PhaseBrief
			in.HostAlive = true
		}), planStartAction{Kind: planRefuseNoTerminal}},
		{"host alive", with(func(in *planStartInput) {
			in.HasState = true
			in.Recorded = plansession.PhaseSlices
			in.HostAlive = true
		}), planStartAction{Kind: planAttach, Reattach: true}},
		{"new folder", planStartInput{Terminal: true},
			planStartAction{Kind: planStart, InitRepo: true, SeedState: true}},
		{"new session in a repository", base,
			planStartAction{Kind: planStart, SeedState: true}},
		{"resume", with(func(in *planStartInput) { in.HasState = true; in.Recorded = plansession.PhaseRounds }),
			planStartAction{Kind: planStart}},
		{"session at draft with a plan", with(func(in *planStartInput) {
			in.HasState = true
			in.Recorded = plansession.PhaseDraft
			in.PlanExists = true
		}), planStartAction{Kind: planStart}},
		{"session at review with a plan", with(func(in *planStartInput) {
			in.HasState = true
			in.Recorded = plansession.PhaseReview
			in.PlanExists = true
		}), planStartAction{Kind: planStart}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := decidePlanStart(tt.in); got != tt.want {
				t.Errorf("decidePlanStart(%+v) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

// TestPlanDryRunInANewFolder: in a folder that is not a repository the dry
// run says it would run `git init` and start a new session, prints the very
// command a start would run, and leaves the folder empty with no outside call
// made. "Empty" is the load-bearing part: a dry run that seeded state.json
// would turn the next real run into a resume.
func TestPlanDryRunInANewFolder(t *testing.T) {
	dir := t.TempDir()
	f := &planFake{} // not a repository, not a terminal
	var so, se bytes.Buffer
	code := runPlanSession([]string{"--dry-run", "--project", dir}, &so, &se, f.deps(t.TempDir()))
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, se.String())
	}
	command := session.ChildCommandLine(session.HostSpawn{Name: session.PlanSessionName, Worktree: dir, MasterPlan: true})
	if command == "" {
		t.Fatal("the default provider gave no planning command")
	}
	for _, want := range []string{"Project: " + dir, "git init", "start a new planning session", command, "Nothing was changed", "not a terminal"} {
		if !strings.Contains(so.String(), want) {
			t.Errorf("dry run output missing %q:\n%s", want, so.String())
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("dry run made outside calls: %v", f.calls)
	}
	if got := folderEntries(t, dir); len(got) != 0 {
		t.Errorf("dry run wrote into the folder: %v", got)
	}
}

// TestPlanDryRunSaysResumeOrReattach covers the other two things a dry run
// can report, and that it uses the phase plansession.Decide gives: the state
// records draft, but slices.md is missing, so the session is at slices.
func TestPlanDryRunSaysResumeOrReattach(t *testing.T) {
	tests := []struct {
		name  string
		alive bool
		want  string
	}{
		{"host stopped", false, "Would resume the planning session"},
		{"host running", true, "Would reattach to the planning session that is already running"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writePlanFile(t, dir, ".afk/plan/state.json", planState(plansession.PhaseModel))
			writePlanFile(t, dir, ".afk/plan/brief.md", "a brief\n")
			before := folderEntries(t, dir)
			f := &planFake{commonDir: filepath.Join(dir, ".git"), alive: tt.alive, tty: true}
			var so, se bytes.Buffer
			if code := runPlanSession([]string{"--dry-run"}, &so, &se, f.deps(dir)); code != 0 {
				t.Fatalf("exit = %d, want 0; stderr: %s", code, se.String())
			}
			// survey.md is missing, so the phase is survey though model is recorded.
			for _, want := range []string{tt.want, "at the survey phase", "`git init` would not be run"} {
				if !strings.Contains(so.String(), want) {
					t.Errorf("output missing %q:\n%s", want, so.String())
				}
			}
			if len(f.calls) != 0 {
				t.Errorf("dry run made outside calls: %v", f.calls)
			}
			if after := folderEntries(t, dir); !reflect.DeepEqual(before, after) {
				t.Errorf("dry run changed the folder: before %v, after %v", before, after)
			}
		})
	}
}

// TestPlanStartInANewFolder pins what a first start does and in which order:
// git init, then the marker and the seeded state (both on disk by the time the
// host is spawned), then the spawn, then the attach. The spawn must be a
// planning spawn and never an unattended one: an unattended planning host
// would wait for ever at its first question.
func TestPlanStartInANewFolder(t *testing.T) {
	dir := t.TempDir()
	f := &planFake{tty: true}
	var so, se bytes.Buffer
	code := runPlanSession(nil, &so, &se, f.deps(dir))
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, se.String())
	}
	wantCalls := []string{"init " + dir, "spawn marker new/brief", "attach started"}
	if !reflect.DeepEqual(f.calls, wantCalls) {
		t.Errorf("calls = %v, want %v", f.calls, wantCalls)
	}
	wantSpec := session.HostSpawn{Name: session.PlanSessionName, Worktree: dir, MasterPlan: true}
	if len(f.spawned) != 1 || f.spawned[0] != wantSpec {
		t.Errorf("spawned = %+v, want one %+v", f.spawned, wantSpec)
	}
	st, ok, err := plansession.ReadState(dir)
	if err != nil || !ok {
		t.Fatalf("state not seeded: ok=%v err=%v", ok, err)
	}
	want := plansession.State{Mode: plansession.ModeNew, Phase: plansession.PhaseBrief, UpdatedAt: "2026-10-13T09:00:00Z"}
	if st != want {
		t.Errorf("state = %+v, want %+v", st, want)
	}
	if !strings.Contains(so.String(), "left the session") {
		t.Errorf("the attach message was not printed: %q", so.String())
	}
}

// TestPlanStartVariants covers the starts and non-starts around the first
// one.
//
// Load-bearing rows: "resume keeps the phase" (a start with a state must not
// seed, or the session would go back to brief); "reattach" (a running host is
// joined, never spawned twice); "no terminal" (nothing is started and nothing
// is written); "plan and no session" (with no terminal nothing is asked or
// started, and the message names the plan's path and the three commands);
// "marker rewritten" (cleaning the session removes the marker, so every start
// writes it again).
func TestPlanStartVariants(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(t *testing.T, dir string)
		fake      planFake
		repo      bool
		args      []string
		wantCode  int
		wantCalls []string
		wantErr   string
		wantPhase string // "" means no state.json afterwards
	}{
		{
			name: "resume keeps the phase",
			setup: func(t *testing.T, dir string) {
				writePlanFile(t, dir, ".afk/plan/state.json", planState(plansession.PhaseSurvey))
				writePlanFile(t, dir, ".afk/plan/brief.md", "a brief\n")
			},
			fake: planFake{tty: true}, repo: true,
			wantCalls: []string{"spawn marker new/survey", "attach started"},
			wantPhase: plansession.PhaseSurvey,
		},
		{
			name: "marker rewritten",
			setup: func(t *testing.T, dir string) {
				writePlanFile(t, dir, ".afk/plan/state.json", planState(plansession.PhaseBrief))
				writePlanFile(t, dir, ".afk/master-plan/host.log", "an earlier run\n")
			},
			fake: planFake{tty: true}, repo: true,
			wantCalls: []string{"spawn marker new/brief", "attach started"},
			wantPhase: plansession.PhaseBrief,
		},
		{
			name: "reattach",
			setup: func(t *testing.T, dir string) {
				writePlanFile(t, dir, ".afk/plan/state.json", planState(plansession.PhaseRounds))
			},
			fake: planFake{tty: true, alive: true}, repo: true,
			wantCalls: []string{"attach running"},
			wantPhase: plansession.PhaseRounds,
		},
		{
			name: "no terminal",
			fake: planFake{}, repo: true,
			wantCode: 1, wantErr: "needs a terminal",
		},
		{
			name:     "no terminal in a new folder",
			fake:     planFake{},
			wantCode: 1, wantErr: "needs a terminal",
		},
		{
			name: "plan and no session",
			setup: func(t *testing.T, dir string) {
				writePlanFile(t, dir, "docs/PLAN.md", "# Plan\n")
			},
			fake: planFake{}, repo: true,
			wantCode: 1, wantErr: "already has a plan at " + filepath.Join("{dir}", "docs", "PLAN.md") + ".\nSay how you want to change it by running one of these in a terminal:\n" + planModeCommands,
		},
		{
			name: "plan named in afk.toml and no session",
			setup: func(t *testing.T, dir string) {
				writePlanFile(t, dir, "afk.toml", "plan = \"notes/build.md\"\n")
				writePlanFile(t, dir, "notes/build.md", "# Plan\n")
			},
			fake: planFake{}, repo: true,
			wantCode: 1, wantErr: filepath.Join("notes", "build.md"),
		},
		{
			name: "damaged state",
			setup: func(t *testing.T, dir string) {
				writePlanFile(t, dir, ".afk/plan/state.json", "{not json")
			},
			fake: planFake{tty: true}, repo: true,
			wantCode: 1, wantErr: "state.json",
		},
		{
			name:     "git init fails",
			fake:     planFake{tty: true, initErr: errors.New("git is not installed")},
			wantCode: 1, wantErr: "git is not installed", wantCalls: []string{"init {dir}"},
		},
		{
			name: "spawn fails",
			fake: planFake{tty: true, spawnErr: errors.New("no afk binary")}, repo: true,
			wantCode: 1, wantErr: "could not start the planning session: no afk binary",
			wantCalls: []string{"spawn marker new/brief"},
			wantPhase: plansession.PhaseBrief,
		},
		{
			name: "unknown provider",
			fake: planFake{tty: true}, repo: true, args: []string{"--provider", "no-such-agent"},
			wantCode: 2, wantErr: "no-such-agent",
		},
		{
			name: "unexpected word after a flag",
			fake: planFake{tty: true}, repo: true, args: []string{"--dry-run", "extra"},
			wantCode: 2, wantErr: `unexpected word "extra"`,
		},
		{
			name: "unknown flag",
			fake: planFake{tty: true}, repo: true, args: []string{"--bogus"},
			wantCode: 2, wantErr: planSessionUsageLine,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.setup != nil {
				tt.setup(t, dir)
			}
			f := tt.fake
			if tt.repo {
				f.commonDir = filepath.Join(dir, ".git")
			}
			var so, se bytes.Buffer
			code := runPlanSession(tt.args, &so, &se, f.deps(dir))
			if code != tt.wantCode {
				t.Fatalf("exit = %d, want %d; stdout: %s stderr: %s", code, tt.wantCode, so.String(), se.String())
			}
			if wantErr := strings.ReplaceAll(tt.wantErr, "{dir}", dir); wantErr != "" && !strings.Contains(se.String(), wantErr) {
				t.Errorf("stderr missing %q:\n%s", wantErr, se.String())
			}
			var wantCalls []string
			for _, c := range tt.wantCalls {
				wantCalls = append(wantCalls, strings.ReplaceAll(c, "{dir}", dir))
			}
			if !reflect.DeepEqual(f.calls, wantCalls) {
				t.Errorf("calls = %v, want %v", f.calls, wantCalls)
			}
			for _, spec := range f.spawned {
				if !spec.MasterPlan || spec.Unattended || spec.ResumeID != "" {
					t.Errorf("spawned %+v, want a planning spawn that is not unattended", spec)
				}
			}
			st, ok, err := plansession.ReadState(dir)
			switch {
			case tt.name == "damaged state":
				if err == nil {
					t.Error("the damaged state.json was replaced")
				}
			case tt.wantPhase == "":
				if ok || err != nil {
					t.Errorf("state.json after the run: ok=%v err=%v state=%+v, want none", ok, err, st)
				}
			case st.Phase != tt.wantPhase:
				t.Errorf("phase after the run = %q, want %q", st.Phase, tt.wantPhase)
			}
			if tt.wantCode != 0 && len(tt.wantCalls) == 0 && session.IsPlanning(dir, session.PlanSessionName) {
				t.Error("a refused run wrote the planning marker")
			}
		})
	}
}

// TestPlanRefusesAProviderWithoutPlanning: an agent that cannot run a
// planning session is refused with exit 1 and the provider's own message,
// before the dry run and with nothing written. The provider is found by
// asking, so the row holds whichever provider that is.
func TestPlanRefusesAProviderWithoutPlanning(t *testing.T) {
	name := refusingProvider(t)
	for _, args := range [][]string{{"--provider", name}, {"--provider", name, "--dry-run"}} {
		dir := t.TempDir()
		f := &planFake{tty: true}
		var so, se bytes.Buffer
		code := runPlanSession(args, &so, &se, f.deps(dir))
		if code != 1 {
			t.Fatalf("%v: exit = %d, want 1; stderr: %s", args, code, se.String())
		}
		if !strings.Contains(se.String(), provider.ErrNoPlanSession.Error()) {
			t.Errorf("%v: stderr missing the provider's message:\n%s", args, se.String())
		}
		if len(f.calls) != 0 || len(folderEntries(t, dir)) != 0 {
			t.Errorf("%v: a refused run did something: calls %v, files %v", args, f.calls, folderEntries(t, dir))
		}
	}
}

// TestPlanProjectIsTheMainCheckout: run from a linked worktree, the planning
// state and the host belong to the main checkout, because that is the one
// place every worktree agrees on. A folder that does not exist is exit 1 and
// names the folder.
func TestPlanProjectIsTheMainCheckout(t *testing.T) {
	main := t.TempDir()
	worktree := t.TempDir()
	f := &planFake{tty: true, commonDir: filepath.Join(main, ".git")}
	var so, se bytes.Buffer
	if code := runPlanSession(nil, &so, &se, f.deps(worktree)); code != 0 {
		t.Fatalf("exit = %d, want 0; stderr: %s", code, se.String())
	}
	if len(f.spawned) != 1 || f.spawned[0].Worktree != main {
		t.Errorf("spawned %+v, want the host in the main checkout %s", f.spawned, main)
	}
	if _, ok, _ := plansession.ReadState(main); !ok {
		t.Error("the state was not seeded in the main checkout")
	}
	if got := folderEntries(t, worktree); len(got) != 0 {
		t.Errorf("the worktree was written to: %v", got)
	}

	missing := filepath.Join(main, "nope")
	for _, run := range []func([]string, *bytes.Buffer, *bytes.Buffer) int{
		func(a []string, so, se *bytes.Buffer) int { return runPlanSession(a, so, se, f.deps(main)) },
		func(a []string, so, se *bytes.Buffer) int { return runPlanStatus(a, so, se, f.deps(main)) },
		func(a []string, so, se *bytes.Buffer) int { return runPlanAdvance(a, so, se, f.deps(main)) },
	} {
		var so, se bytes.Buffer
		if code := run([]string{"--project", missing}, &so, &se); code != 1 {
			t.Errorf("missing folder: exit = %d, want 1", code)
		}
		if !strings.Contains(se.String(), missing+" was not found") {
			t.Errorf("missing folder: stderr does not name it:\n%s", se.String())
		}
	}
}

// TestPlanStatus pins what `afk plan status` prints and its exit code.
//
// Load-bearing rows: "no session" (exit 0 and how to start one, in a folder
// that is not a repository, with nothing written and no git init);
// "pulled back" (the phase is the earlier unfinished one, and the line says
// what was recorded); "damaged state" and "question problem" (exit 1, each
// problem on its own line); "check counts plan errors" (the checker's count
// reaches the decision).
func TestPlanStatus(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(t *testing.T, dir string)
		lint     int
		args     []string
		wantCode int
		want     []string
		wantErr  string
	}{
		{
			name: "no session",
			want: []string{"There is no planning session here", "Run `afk plan`"},
		},
		{
			name: "mid session",
			setup: func(t *testing.T, dir string) {
				writePlanFile(t, dir, ".afk/plan/state.json", planState(plansession.PhaseSurvey))
				writePlanFile(t, dir, ".afk/plan/brief.md", "a brief\n")
			},
			want: []string{"Mode: new\n", "Phase: survey\n", "Waiting for: The survey of the folder"},
		},
		{
			name: "pulled back",
			setup: func(t *testing.T, dir string) {
				writePlanFile(t, dir, ".afk/plan/state.json", planState(plansession.PhaseModel))
			},
			want: []string{"Phase: brief (the session had reached model", "Waiting for: Your request has not been saved"},
		},
		{
			name: "open questions",
			setup: func(t *testing.T, dir string) {
				writePlanFile(t, dir, ".afk/plan/state.json", planState(plansession.PhaseRounds))
				writePlanFile(t, dir, ".afk/plan/brief.md", "a brief\n")
				writePlanFile(t, dir, ".afk/plan/survey.md", "a survey\n")
				writePlanFile(t, dir, ".afk/plan/questions.jsonl",
					`{"id":"q1","round":1,"kind":"setup","question":"Which domain?"}`+"\n")
			},
			want: []string{"Phase: rounds\n", "1 question of 1 has no answer yet"},
		},
		{
			name: "check counts plan errors",
			setup: func(t *testing.T, dir string) {
				writePlanFile(t, dir, ".afk/plan/state.json", planState(plansession.PhaseCheck))
				writePlanFile(t, dir, ".afk/plan/brief.md", "a brief\n")
				writePlanFile(t, dir, ".afk/plan/survey.md", "a survey\n")
				writePlanFile(t, dir, ".afk/plan/slices.md", "slices\n")
				writePlanFile(t, dir, ".afk/plan/questions.jsonl", planQuestions("no: a command line"))
				writePlanFile(t, dir, "docs/PLAN.md", "# Plan\n")
			},
			lint: 3,
			want: []string{"Phase: check\n", "3 errors"},
		},
		{
			name: "damaged state",
			setup: func(t *testing.T, dir string) {
				writePlanFile(t, dir, ".afk/plan/state.json", "{not json")
			},
			wantCode: 1,
			want:     []string{"Phase: unknown\n", "Problem: ", "state.json"},
		},
		{
			name: "question problem",
			setup: func(t *testing.T, dir string) {
				writePlanFile(t, dir, ".afk/plan/state.json", planState(plansession.PhaseBrief))
				writePlanFile(t, dir, ".afk/plan/questions.jsonl", "not json\n")
			},
			wantCode: 1,
			want:     []string{"Phase: brief\n", "Problem: line 1"},
		},
		{name: "unknown flag", args: []string{"--bogus"}, wantCode: 2, wantErr: planStatusUsageLine},
		{name: "unexpected word", args: []string{"now"}, wantCode: 2, wantErr: `unexpected word "now"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.setup != nil {
				tt.setup(t, dir)
			}
			before := folderEntries(t, dir)
			f := &planFake{lint: tt.lint} // not a repository
			var so, se bytes.Buffer
			code := runPlanStatus(tt.args, &so, &se, f.deps(dir))
			if code != tt.wantCode {
				t.Fatalf("exit = %d, want %d; stdout: %s stderr: %s", code, tt.wantCode, so.String(), se.String())
			}
			for _, want := range tt.want {
				if !strings.Contains(so.String(), want) {
					t.Errorf("stdout missing %q:\n%s", want, so.String())
				}
			}
			if tt.wantErr != "" && !strings.Contains(se.String(), tt.wantErr) {
				t.Errorf("stderr missing %q:\n%s", tt.wantErr, se.String())
			}
			if len(f.calls) != 0 {
				t.Errorf("status made outside calls: %v", f.calls)
			}
			if after := folderEntries(t, dir); !reflect.DeepEqual(before, after) {
				t.Errorf("status changed the folder: before %v, after %v", before, after)
			}
		})
	}
}

// TestPlanAdvance pins `afk plan advance`: it writes the next phase only when
// plansession.Advance allows it.
//
// Load-bearing rows: "gate not met" (exit 1, the reason printed and
// state.json byte-identical: a refused advance must not even touch the time);
// "gate met" (the exact `Phase: brief → survey` line, the new phase and the
// clock's time on disk, the mode kept); "no session" (advance never seeds a
// session; only `afk plan` does).
func TestPlanAdvance(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(t *testing.T, dir string)
		args      []string
		wantCode  int
		wantOut   string
		wantErr   string
		wantPhase string // "" means state.json must be unchanged
	}{
		{
			name: "gate met",
			setup: func(t *testing.T, dir string) {
				writePlanFile(t, dir, ".afk/plan/state.json", planState(plansession.PhaseBrief))
				writePlanFile(t, dir, ".afk/plan/brief.md", "a brief\n")
			},
			wantOut:   "Phase: brief → survey\n",
			wantPhase: plansession.PhaseSurvey,
		},
		{
			name: "gate not met",
			setup: func(t *testing.T, dir string) {
				writePlanFile(t, dir, ".afk/plan/state.json", planState(plansession.PhaseBrief))
			},
			wantCode: 1,
			wantErr:  "brief.md is missing or empty",
		},
		{
			name: "earlier phase unfinished",
			setup: func(t *testing.T, dir string) {
				writePlanFile(t, dir, ".afk/plan/state.json", planState(plansession.PhaseModel))
				writePlanFile(t, dir, ".afk/plan/brief.md", "a brief\n")
			},
			wantCode: 1,
			wantErr:  "the earlier survey phase is not finished",
		},
		{
			name: "review stops for approval",
			setup: func(t *testing.T, dir string) {
				writePlanFile(t, dir, ".afk/plan/state.json", planState(plansession.PhaseReview))
				writePlanFile(t, dir, ".afk/plan/brief.md", "a brief\n")
				writePlanFile(t, dir, ".afk/plan/survey.md", "a survey\n")
				writePlanFile(t, dir, ".afk/plan/slices.md", "slices\n")
				writePlanFile(t, dir, ".afk/plan/critic.md", "critic\n")
				writePlanFile(t, dir, ".afk/plan/questions.jsonl", planQuestions("no: a command line"))
				writePlanFile(t, dir, "docs/PLAN.md", "# Plan\n")
			},
			wantCode: 1,
			wantErr:  "afk plan approve",
		},
		{name: "no session", wantCode: 1, wantErr: "There is no planning session here"},
		{
			name: "damaged state",
			setup: func(t *testing.T, dir string) {
				writePlanFile(t, dir, ".afk/plan/state.json", "{not json")
			},
			wantCode: 1,
			wantErr:  "state.json",
		},
		{name: "unknown flag", args: []string{"--bogus"}, wantCode: 2, wantErr: planAdvanceUsageLine},
		{name: "unexpected word", args: []string{"survey"}, wantCode: 2, wantErr: `unexpected word "survey"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.setup != nil {
				tt.setup(t, dir)
			}
			statePath := plansession.StatePath(dir)
			before, _ := os.ReadFile(statePath)
			f := &planFake{}
			var so, se bytes.Buffer
			code := runPlanAdvance(tt.args, &so, &se, f.deps(dir))
			if code != tt.wantCode {
				t.Fatalf("exit = %d, want %d; stdout: %s stderr: %s", code, tt.wantCode, so.String(), se.String())
			}
			if tt.wantOut != "" && !strings.HasPrefix(so.String(), tt.wantOut) {
				t.Errorf("stdout = %q, want it to start with %q", so.String(), tt.wantOut)
			}
			if tt.wantErr != "" && !strings.Contains(se.String(), tt.wantErr) {
				t.Errorf("stderr missing %q:\n%s", tt.wantErr, se.String())
			}
			after, _ := os.ReadFile(statePath)
			if tt.wantPhase == "" {
				if !bytes.Equal(before, after) {
					t.Errorf("state.json changed on a refused advance:\nbefore %q\nafter  %q", before, after)
				}
				return
			}
			st, ok, err := plansession.ReadState(dir)
			if err != nil || !ok {
				t.Fatalf("state after advance: ok=%v err=%v", ok, err)
			}
			want := plansession.State{Mode: plansession.ModeNew, Phase: tt.wantPhase, UpdatedAt: "2026-10-13T09:00:00Z"}
			if st != want {
				t.Errorf("state = %+v, want %+v", st, want)
			}
			if !strings.Contains(so.String(), "Waiting for: ") {
				t.Errorf("advance did not say what the new phase waits for:\n%s", so.String())
			}
		})
	}
}

// folderFiles reads every file under dir, so a test can say "nothing was
// changed" about contents as well as names.
func folderFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	got := map[string]string{}
	for _, rel := range folderEntries(t, dir) {
		path := filepath.Join(dir, rel)
		if fi, err := os.Stat(path); err != nil || fi.IsDir() {
			got[rel] = "<folder>"
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		got[rel] = string(data)
	}
	return got
}

// planWithFinishedChange sets up a project with a plan and a change session
// that is over: an amend session at review, its files still in .afk/plan/.
func planWithFinishedChange(t *testing.T, dir string) {
	t.Helper()
	writePlanFile(t, dir, "docs/PLAN.md", "# Plan\n")
	writePlanFile(t, dir, ".afk/plan/state.json", `{"mode":"amend","phase":"review","updated_at":"2026-10-12T00:00:00Z"}`)
	writePlanFile(t, dir, ".afk/plan/brief.md", "the earlier request\n")
	writePlanFile(t, dir, ".afk/plan/changes.md", "- added T1.3\n")
}

// TestRunPlanMode pins `afk plan extend`, `amend` and `reconcile`: what each
// seeds, and that every refusal leaves the project byte for byte as it was.
//
// Load-bearing rows:
//   - "open session": the state and files of a session in progress are
//     untouched. A change session that seeded over it would lose the owner's
//     answers.
//   - "finished session is archived": the old brief ends up under archive/
//     and is not overwritten by the new one.
//   - "amend writes the brief word for word": the owner's text reaches the
//     session unchanged, double space and punctuation included.
//   - "reconcile, missing file", "reconcile, file outside the project" and
//     "no terminal": exit 1 with nothing seeded, so the same command can be
//     run again once the cause is fixed.
//   - "dry run writes nothing": a dry run that seeded a state would make the
//     next real command a refusal ("a session is already open").
//   - "extend rejects --yes": --yes belongs to amend only and is not
//     silently accepted elsewhere.
func TestRunPlanMode(t *testing.T) {
	const archive = ".afk/plan/archive/20261013T090000Z/"
	plan := func(t *testing.T, dir string) { writePlanFile(t, dir, "docs/PLAN.md", "# Plan\n") }
	planAndDoc := func(t *testing.T, dir string) {
		plan(t, dir)
		writePlanFile(t, dir, "docs/design-system.md", "# Design\n")
	}
	tests := []struct {
		name  string
		setup func(t *testing.T, dir string)
		fake  planFake
		// elsewhere runs the command from another folder with --project.
		elsewhere bool
		// args may hold {dir} (the project) and {other} (a folder outside it).
		args      []string
		wantCode  int
		wantErr   string
		wantOut   []string
		wantCalls []string
		// wantFiles are files under the project and their exact content after
		// the run; absent are paths that must not exist.
		wantFiles map[string]string
		absent    []string
		// unchanged: the project folder is byte for byte as before.
		unchanged bool
	}{
		{
			name: "extend seeds the mode", setup: plan, fake: planFake{tty: true},
			args:      []string{"extend"},
			wantCalls: []string{"spawn marker extend/brief", "attach started"},
			wantFiles: map[string]string{".afk/plan/state.json": "{\n  \"mode\": \"extend\",\n  \"phase\": \"brief\",\n  \"updated_at\": \"2026-10-13T09:00:00Z\"\n}\n"},
			absent:    []string{".afk/plan/brief.md", ".afk/plan/yes", ".afk/plan/archive"},
		},
		{
			name: "amend writes the brief word for word", setup: plan, fake: planFake{tty: true},
			args:      []string{"amend", "Add a task:  export CSV,", "please."},
			wantCalls: []string{"spawn marker amend/brief", "attach started"},
			wantFiles: map[string]string{".afk/plan/brief.md": "Add a task:  export CSV, please.\n"},
			absent:    []string{".afk/plan/yes"},
		},
		{
			name: "amend --yes writes the marker", setup: plan, fake: planFake{tty: true},
			args:      []string{"amend", "rename the export task", "--yes"},
			wantCalls: []string{"spawn marker amend/brief", "attach started"},
			wantFiles: map[string]string{".afk/plan/brief.md": "rename the export task\n", ".afk/plan/yes": ""},
		},
		{
			name: "amend, flags between and the project named from elsewhere", setup: plan, fake: planFake{tty: true},
			elsewhere: true,
			args:      []string{"amend", "--yes", "split", "--project", "{dir}", "T1.2"},
			wantCalls: []string{"spawn marker amend/brief", "attach started"},
			wantFiles: map[string]string{".afk/plan/brief.md": "split T1.2\n", ".afk/plan/yes": ""},
		},
		{
			name: "reconcile names the file in the brief", setup: planAndDoc, fake: planFake{tty: true},
			args:      []string{"reconcile", "docs/design-system.md"},
			wantCalls: []string{"spawn marker reconcile/brief", "attach started"},
			wantFiles: map[string]string{".afk/plan/brief.md": "Reconcile the plan with docs/design-system.md.\n"},
		},
		{
			name: "reconcile, absolute path inside the project", setup: planAndDoc, fake: planFake{tty: true},
			args:      []string{"reconcile", filepath.Join("{dir}", "docs", "design-system.md")},
			wantCalls: []string{"spawn marker reconcile/brief", "attach started"},
			wantFiles: map[string]string{".afk/plan/brief.md": "Reconcile the plan with docs/design-system.md.\n"},
		},
		{
			name: "reconcile from elsewhere finds the file in the project", setup: planAndDoc, fake: planFake{tty: true},
			elsewhere: true,
			args:      []string{"reconcile", "--project", "{dir}", "docs/design-system.md"},
			wantCalls: []string{"spawn marker reconcile/brief", "attach started"},
			wantFiles: map[string]string{".afk/plan/brief.md": "Reconcile the plan with docs/design-system.md.\n"},
		},
		{
			name: "reconcile, missing file", setup: plan, fake: planFake{tty: true},
			args:     []string{"reconcile", "missing.md"},
			wantCode: 1, wantErr: "the file missing.md was not found", unchanged: true,
		},
		{
			name: "reconcile, missing file, dry run", setup: plan, fake: planFake{tty: true},
			args:     []string{"reconcile", "--dry-run", "missing.md"},
			wantCode: 1, wantErr: "the file missing.md was not found", unchanged: true,
		},
		{
			name: "reconcile, file outside the project", setup: plan, fake: planFake{tty: true},
			args:     []string{"reconcile", filepath.Join("{other}", "notes.md")},
			wantCode: 1, wantErr: "is outside the project folder {dir}", unchanged: true,
		},
		{
			name: "reconcile, relative path that leaves the project", setup: plan, fake: planFake{tty: true},
			elsewhere: true,
			args:      []string{"reconcile", "--project", "{dir}", "notes.md"},
			wantCode:  1, wantErr: filepath.Join("{other}", "notes.md") + " is outside the project folder", unchanged: true,
		},
		{
			name: "reconcile, a folder", setup: planAndDoc, fake: planFake{tty: true},
			args:     []string{"reconcile", "docs"},
			wantCode: 1, wantErr: "is a folder, not a file", unchanged: true,
		},
		{
			name: "finished session is archived", setup: planWithFinishedChange, fake: planFake{tty: true},
			args:      []string{"amend", "a second change"},
			wantCalls: []string{"spawn marker amend/brief", "attach started"},
			wantOut:   []string{"The earlier planning session's files were moved to " + filepath.Join("{dir}", ".afk", "plan", "archive", "20261013T090000Z") + "."},
			wantFiles: map[string]string{
				".afk/plan/brief.md":   "a second change\n",
				archive + "brief.md":   "the earlier request\n",
				archive + "changes.md": "- added T1.3\n",
				archive + "state.json": `{"mode":"amend","phase":"review","updated_at":"2026-10-12T00:00:00Z"}`,
				".afk/plan/state.json": "{\n  \"mode\": \"amend\",\n  \"phase\": \"brief\",\n  \"updated_at\": \"2026-10-13T09:00:00Z\"\n}\n",
			},
			absent: []string{".afk/plan/changes.md"},
		},
		{
			name: "no plan", fake: planFake{tty: true},
			args:     []string{"extend"},
			wantCode: 1, wantErr: "Run `afk plan` first", unchanged: true,
		},
		{
			name: "open session",
			setup: func(t *testing.T, dir string) {
				plan(t, dir)
				writePlanFile(t, dir, ".afk/plan/state.json", planState(plansession.PhaseRounds))
				writePlanFile(t, dir, ".afk/plan/brief.md", "a brief\n")
				writePlanFile(t, dir, ".afk/plan/survey.md", "a survey\n")
			},
			fake:     planFake{tty: true},
			args:     []string{"amend", "something else"},
			wantCode: 1, wantErr: "It is in the new mode, at the rounds phase.\nRun `afk plan` to go back to it.", unchanged: true,
		},
		{
			name: "open session, dry run",
			setup: func(t *testing.T, dir string) {
				plan(t, dir)
				writePlanFile(t, dir, ".afk/plan/state.json", `{"mode":"extend","phase":"brief","updated_at":"2026-10-12T00:00:00Z"}`)
			},
			fake:     planFake{tty: true},
			args:     []string{"extend", "--dry-run"},
			wantCode: 1, wantErr: "It is in the extend mode, at the brief phase.", unchanged: true,
		},
		{
			name: "agent of the finished session still running", setup: planWithFinishedChange, fake: planFake{tty: true, alive: true},
			args:     []string{"extend"},
			wantCode: 1, wantErr: "run `afk clean " + session.PlanSessionName + "`, then run this command again", unchanged: true,
		},
		{
			name: "no terminal", setup: planWithFinishedChange, fake: planFake{},
			args:     []string{"amend", "a change"},
			wantCode: 1, wantErr: "needs a terminal", unchanged: true,
		},
		{
			name: "dry run writes nothing", setup: planWithFinishedChange, fake: planFake{},
			args: []string{"extend", "--dry-run"},
			wantOut: []string{"Would move the earlier planning session's files", "Would start a new planning session (mode: extend) at the brief phase",
				"Would start: ", "`afk plan extend` would stop here", "This was a dry run. Nothing was changed."},
			unchanged: true,
		},
		{
			name: "amend dry run shows the brief", setup: plan, fake: planFake{tty: true},
			args: []string{"amend", "--yes", "--dry-run", "add a task that exports CSV"},
			wantOut: []string{"(mode: amend)", "Would save this as the session's brief: add a task that exports CSV\n",
				"you already agreed to the change (--yes)", "Nothing was changed."},
			unchanged: true,
		},
		{
			name: "reconcile dry run names the file", setup: planAndDoc, fake: planFake{tty: true},
			args:      []string{"reconcile", "docs/design-system.md", "--dry-run"},
			wantOut:   []string{"(mode: reconcile)", "Reconcile the plan with docs/design-system.md.\n"},
			unchanged: true,
		},
		{
			name: "amend with no text", setup: plan, fake: planFake{tty: true},
			args:     []string{"amend"},
			wantCode: 2, wantErr: "Usage: " + planAmendUsageLine, unchanged: true,
		},
		{
			name: "amend with blank text", setup: plan, fake: planFake{tty: true},
			args:     []string{"amend", "  ", ""},
			wantCode: 2, wantErr: "say what to change", unchanged: true,
		},
		{
			name: "reconcile with no file", setup: plan, fake: planFake{tty: true},
			args:     []string{"reconcile"},
			wantCode: 2, wantErr: "Usage: " + planReconcileUsageLine, unchanged: true,
		},
		{
			name: "reconcile with two files", setup: planAndDoc, fake: planFake{tty: true},
			args:     []string{"reconcile", "docs/design-system.md", "docs/PLAN.md"},
			wantCode: 2, wantErr: "expected one file", unchanged: true,
		},
		{
			name: "extend with a word", setup: plan, fake: planFake{tty: true},
			args:     []string{"extend", "billing"},
			wantCode: 2, wantErr: `unexpected word "billing"`, unchanged: true,
		},
		{
			name: "extend rejects --yes", setup: plan, fake: planFake{tty: true},
			args:     []string{"extend", "--yes"},
			wantCode: 2, wantErr: planExtendUsageLine, unchanged: true,
		},
		{
			name: "unknown provider", setup: plan, fake: planFake{tty: true},
			args:     []string{"extend", "--provider", "no-such-agent"},
			wantCode: 2, wantErr: "no-such-agent", unchanged: true,
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
			cwd := dir
			if tt.elsewhere {
				cwd = other
			}
			var args []string
			for _, a := range tt.args[1:] {
				args = append(args, fill(a))
			}
			var so, se bytes.Buffer
			code := runPlanMode(tt.args[0], args, &so, &se, f.deps(cwd))
			if code != tt.wantCode {
				t.Fatalf("exit = %d, want %d; stdout: %s stderr: %s", code, tt.wantCode, so.String(), se.String())
			}
			if want := fill(tt.wantErr); want != "" && !strings.Contains(se.String(), want) {
				t.Errorf("stderr missing %q:\n%s", want, se.String())
			}
			if tt.wantCode == 1 && !strings.Contains(se.String(), "Nothing was started.") {
				t.Errorf("a refusal did not say that nothing was started:\n%s", se.String())
			}
			for _, want := range tt.wantOut {
				if !strings.Contains(so.String(), fill(want)) {
					t.Errorf("stdout missing %q:\n%s", fill(want), so.String())
				}
			}
			if !reflect.DeepEqual(f.calls, tt.wantCalls) {
				t.Errorf("calls = %v, want %v", f.calls, tt.wantCalls)
			}
			for _, spec := range f.spawned {
				if !spec.MasterPlan || spec.Unattended || spec.ResumeID != "" || spec.Worktree != dir {
					t.Errorf("spawned %+v, want a planning spawn in the project that is not unattended", spec)
				}
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
			if len(f.asked) != 0 {
				t.Errorf("a mode command asked a question: %q", f.asked)
			}
		})
	}
}

// TestPlanChoosesAMode pins a bare `afk plan` on a project that has a plan:
// it asks, and then does what the matching command does.
//
// Load-bearing rows: "empty answer", "unknown answer" and "amend with no
// text" (nothing is started and nothing is written; the three commands are
// listed, so the owner is not left guessing); "2 then the text" (the second
// answer is the brief, word for word); "3 then a missing file" (the answer is
// checked exactly as the command's argument is).
func TestPlanChoosesAMode(t *testing.T) {
	tests := []struct {
		name      string
		answers   []string
		wantCode  int
		wantErr   string
		wantState string // mode/phase, "" for no state.json
		wantBrief string
		wantAsked int
	}{
		{"1", []string{"1"}, 0, "", "extend/brief", "", 1},
		{"the word extend", []string{" Extend "}, 0, "", "extend/brief", "", 1},
		{"2 then the text", []string{"2", "add a task that  exports CSV"}, 0, "", "amend/brief", "add a task that  exports CSV\n", 2},
		{"3 then the file", []string{"3", " docs/design-system.md "}, 0, "", "reconcile/brief", "Reconcile the plan with docs/design-system.md.\n", 2},
		{"empty answer", []string{""}, 1, "You did not pick one. Nothing was started.", "", "", 1},
		{"unknown answer", []string{"4"}, 1, `"4" is not one of the three choices. Nothing was started.`, "", "", 1},
		{"amend with no text", []string{"2", "  "}, 1, "You did not say what to change. Nothing was started.", "", "", 2},
		{"reconcile with no file", []string{"3", ""}, 1, "You did not name a document. Nothing was started.", "", "", 2},
		{"3 then a missing file", []string{"3", "missing.md"}, 1, "the file missing.md was not found", "", "", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writePlanFile(t, dir, "docs/PLAN.md", "# Plan\n")
			writePlanFile(t, dir, "docs/design-system.md", "# Design\n")
			before := folderFiles(t, dir)
			f := &planFake{tty: true, commonDir: filepath.Join(dir, ".git"), answers: tt.answers}
			var so, se bytes.Buffer
			code := runPlanSession(nil, &so, &se, f.deps(dir))
			if code != tt.wantCode {
				t.Fatalf("exit = %d, want %d; stdout: %s stderr: %s", code, tt.wantCode, so.String(), se.String())
			}
			if len(f.asked) != tt.wantAsked {
				t.Fatalf("asked %d question(s), want %d: %q", len(f.asked), tt.wantAsked, f.asked)
			}
			for _, want := range []string{"already has a plan at " + filepath.Join(dir, "docs", "PLAN.md"),
				"1) add a milestone (extend)", "2) add, split or replace one task (amend)", "3) bring the plan in line with a document (reconcile)"} {
				if !strings.Contains(f.asked[0], want) {
					t.Errorf("the question is missing %q:\n%s", want, f.asked[0])
				}
			}
			if tt.wantErr != "" && !strings.Contains(se.String(), tt.wantErr) {
				t.Errorf("stderr missing %q:\n%s", tt.wantErr, se.String())
			}
			if tt.wantCode != 0 {
				if !reflect.DeepEqual(before, folderFiles(t, dir)) {
					t.Errorf("a run that started nothing changed the project: %v", folderEntries(t, dir))
				}
				if len(f.calls) != 0 {
					t.Errorf("a run that started nothing made outside calls: %v", f.calls)
				}
				// The file refusal is the command's own message; the others
				// list the three commands.
				if !strings.Contains(tt.wantErr, "missing.md") && !strings.Contains(se.String(), planModeCommands) {
					t.Errorf("stderr does not list the three commands:\n%s", se.String())
				}
				return
			}
			wantCalls := []string{"spawn marker " + tt.wantState, "attach started"}
			if !reflect.DeepEqual(f.calls, wantCalls) {
				t.Errorf("calls = %v, want %v", f.calls, wantCalls)
			}
			brief, _ := os.ReadFile(filepath.Join(dir, ".afk", "plan", "brief.md"))
			if string(brief) != tt.wantBrief {
				t.Errorf("brief.md = %q, want %q", brief, tt.wantBrief)
			}
		})
	}
}

// TestPlanWithAPlanAsksNothingWithoutATerminalOrInADryRun: the question is
// only ever put to somebody who can answer. A dry run says the real command
// would ask and exits 0; without a terminal the command exits 1; both list
// the three commands, ask nothing and leave the project as it was. The same
// holds when the last change session is finished, which is how the owner
// comes back to change the plan a second time.
func TestPlanWithAPlanAsksNothingWithoutATerminalOrInADryRun(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(t *testing.T, dir string)
		tty      bool
		args     []string
		wantCode int
		want     string // on stdout for exit 0, on stderr otherwise
	}{
		{"dry run", nil, true, []string{"--dry-run"}, 0, "would ask how you want to change it"},
		{"dry run without a terminal", nil, false, []string{"--dry-run"}, 0, "there is nobody to ask"},
		{"no terminal", nil, false, nil, 1, "Nothing was started."},
		{"finished change session, no terminal", planWithFinishedChange, false, nil, 1, "Nothing was started."},
		{"finished change session, dry run", planWithFinishedChange, true, []string{"--dry-run"}, 0, "would ask how you want to change it"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writePlanFile(t, dir, "docs/PLAN.md", "# Plan\n")
			if tt.setup != nil {
				tt.setup(t, dir)
			}
			before := folderFiles(t, dir)
			f := &planFake{tty: tt.tty, commonDir: filepath.Join(dir, ".git"), answers: []string{"1"}}
			var so, se bytes.Buffer
			code := runPlanSession(tt.args, &so, &se, f.deps(dir))
			if code != tt.wantCode {
				t.Fatalf("exit = %d, want %d; stdout: %s stderr: %s", code, tt.wantCode, so.String(), se.String())
			}
			out := so.String()
			if code != 0 {
				out = se.String()
			}
			for _, want := range []string{tt.want, planModeCommands, filepath.Join(dir, "docs", "PLAN.md")} {
				if !strings.Contains(out, want) {
					t.Errorf("output missing %q:\n%s", want, out)
				}
			}
			if len(f.asked) != 0 || len(f.calls) != 0 {
				t.Errorf("asked %q and called %v, want neither", f.asked, f.calls)
			}
			if !reflect.DeepEqual(before, folderFiles(t, dir)) {
				t.Errorf("the project was changed: %v", folderEntries(t, dir))
			}
		})
	}
}

// TestPlanResumesAnOpenChangeSession: a bare `afk plan` goes back to a change
// session that is not finished, whatever its mode, without asking and without
// touching its files. This is the other half of the open-session refusal: the
// refusal tells the owner to run `afk plan`, so `afk plan` must take them
// there.
func TestPlanResumesAnOpenChangeSession(t *testing.T) {
	for _, mode := range []string{plansession.ModeExtend, plansession.ModeAmend, plansession.ModeReconcile} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			writePlanFile(t, dir, "docs/PLAN.md", "# Plan\n")
			state := `{"mode":"` + mode + `","phase":"survey","updated_at":"2026-10-12T00:00:00Z"}`
			writePlanFile(t, dir, ".afk/plan/state.json", state)
			writePlanFile(t, dir, ".afk/plan/brief.md", "a brief\n")
			f := &planFake{tty: true, commonDir: filepath.Join(dir, ".git")}
			var so, se bytes.Buffer
			if code := runPlanSession(nil, &so, &se, f.deps(dir)); code != 0 {
				t.Fatalf("exit = %d, want 0; stderr: %s", code, se.String())
			}
			wantCalls := []string{"spawn marker " + mode + "/survey", "attach started"}
			if !reflect.DeepEqual(f.calls, wantCalls) {
				t.Errorf("calls = %v, want %v", f.calls, wantCalls)
			}
			if got, _ := os.ReadFile(plansession.StatePath(dir)); string(got) != state {
				t.Errorf("state.json was rewritten: %q", got)
			}
			if len(f.asked) != 0 {
				t.Errorf("a resume asked a question: %q", f.asked)
			}

			// And `afk plan status` shows which kind of session it is.
			so.Reset()
			if code := runPlanStatus(nil, &so, &se, f.deps(dir)); code != 0 {
				t.Fatalf("status exit = %d; stderr: %s", code, se.String())
			}
			if !strings.Contains(so.String(), "Mode: "+mode+"\n") {
				t.Errorf("status does not show the mode:\n%s", so.String())
			}
		})
	}
}

// TestPlanRefusesAFinishedSessionWhosePlanIsGone: the plan a finished session
// wrote is not where afk looks for it. Starting over would write a second
// plan beside one that was only moved, so the command stops and names the
// place it looked.
func TestPlanRefusesAFinishedSessionWhosePlanIsGone(t *testing.T) {
	dir := t.TempDir()
	writePlanFile(t, dir, ".afk/plan/state.json", planState(plansession.PhaseApproved))
	before := folderFiles(t, dir)
	f := &planFake{tty: true, commonDir: filepath.Join(dir, ".git")}
	var so, se bytes.Buffer
	if code := runPlanSession(nil, &so, &se, f.deps(dir)); code != 1 {
		t.Fatalf("exit = %d, want 1; stderr: %s", code, se.String())
	}
	for _, want := range []string{"its plan is not at " + filepath.Join(dir, "docs", "PLAN.md"), "Nothing was started."} {
		if !strings.Contains(se.String(), want) {
			t.Errorf("stderr missing %q:\n%s", want, se.String())
		}
	}
	if len(f.calls) != 0 || !reflect.DeepEqual(before, folderFiles(t, dir)) {
		t.Errorf("a refused run did something: calls %v, files %v", f.calls, folderEntries(t, dir))
	}
}

// planFixture reads one file of internal/planpatch's testdata. The design
// fixtures are written and checked by that package's tests; reading the same
// files here is what ties the commands to them.
func planFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("internal", "planpatch", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// realLintDeps is the fake's deps with the real plan checker put back, so a
// fixture session is held to what `afk plan lint` says of its plan and not to
// a number the test chose.
func realLintDeps(f *planFake, dir string) planDeps {
	deps := f.deps(dir)
	deps.lintErrors = func(plan []byte) int { return planlint.Lint(plan).Summary.Errors }
	return deps
}

// seedPlanSession writes a new session's files up to and including the check
// phase, with the recorded phase and the interface answer given, and the plan.
func seedPlanSession(t *testing.T, dir, phase, interfaceAnswer, plan string) {
	t.Helper()
	writePlanFile(t, dir, ".afk/plan/state.json", planState(phase))
	writePlanFile(t, dir, ".afk/plan/brief.md", "a recipe box\n")
	writePlanFile(t, dir, ".afk/plan/survey.md", "an empty folder\n")
	writePlanFile(t, dir, ".afk/plan/questions.jsonl", planQuestions(interfaceAnswer))
	writePlanFile(t, dir, ".afk/plan/slices.md", "two milestones\n")
	writePlanFile(t, dir, ".afk/plan/critic.md", "no finding left open\n")
	writePlanFile(t, dir, "docs/PLAN.md", plan)
}

// designRuleSentence is the header line the design fixture adds to the base
// plan, taken from the fixture so the two cannot drift.
func designRuleSentence(t *testing.T) string {
	t.Helper()
	for _, line := range strings.Split(planFixture(t, "design.golden.md"), "\n") {
		if strings.Contains(line, "wins on looks") {
			return line + "\n"
		}
	}
	t.Fatal("design.golden.md has no line with the precedence rule")
	return ""
}

// basePlanWithRule is the base plan with the precedence sentence in its
// header and nothing else of the design phase: no design task, no extra
// dependency.
func basePlanWithRule(t *testing.T) string {
	t.Helper()
	base := planFixture(t, "base-plan.md")
	at := strings.Index(base, "\n## ")
	if at < 0 {
		t.Fatal("base-plan.md has no ## heading")
	}
	// The header ends with a blank line before the first section; the
	// sentence goes on the line before that blank one.
	return base[:at] + designRuleSentence(t) + base[at:]
}

// TestDesignFacts is the reading of a plan the design gate is built on.
//
// Load-bearing rows:
//   - "the design fixture" and "the base plan": the two end states of a
//     planning session (with an interface, and without) read as all four
//     facts and as none.
//   - "the rule under a later heading": only the header counts, or a task
//     that mentions the design system would pass for the plan's rule.
//   - "one phrase missing" (three rows): each of the three texts is needed.
//   - "the rule across a line break and in capitals": the header is prose, so
//     a wrapped line must not make the gate refuse a plan that states it.
//   - "an agent's design task" and "a design task for one area": the task is
//     found by masterplan's narrow test, so neither is taken for it.
//   - "the task in a later milestone" and "a plan that starts at M0": first
//     means the milestone of the file's first task, whatever its number.
//   - "a plan that does not parse": every fact is zero, the rule included, so
//     nothing is read from half a plan.
func TestDesignFacts(t *testing.T) {
	const rule = "Where this plan and `docs/design-system.md` disagree, the plan wins on behaviour and the design system wins on looks.\n"
	task := func(id, title, deps string) string {
		return "#### " + id + " — " + title + "\n- **Depends on:** " + deps + "\n- **Goal:** x\n\n"
	}
	plan := func(header string, tasks ...string) string {
		return "# Plan\n\n**How to use this document.** Read it.\n" + header + "\n## Milestones\n\n### M1 — First\n\n" + strings.Join(tasks, "")
	}
	design := task("T1.2", "Design system (owner)", "—")

	tests := []struct {
		name           string
		plan           string
		wantRule       bool
		wantTask       string
		wantFirst      bool
		wantDependents int
	}{
		{name: "the design fixture", plan: planFixture(t, "design.golden.md"),
			wantRule: true, wantTask: "T1.4", wantFirst: true, wantDependents: 4},
		{name: "the reconcile fixture", plan: planFixture(t, "reconcile-design.golden.md"),
			wantRule: true, wantTask: "T1.4", wantFirst: true, wantDependents: 4},
		{name: "the base plan", plan: planFixture(t, "base-plan.md")},
		{name: "the rule and nothing else", plan: plan(rule, task("T1.1", "Store a recipe", "—")), wantRule: true},
		{name: "the rule under a later heading",
			plan: plan("", task("T1.1", "Store a recipe", "—")+"## Notes\n\n"+rule)},
		{name: "one phrase missing: the file",
			plan: plan("The plan wins on behaviour and the design system wins on looks.\n", task("T1.1", "A", "—"))},
		{name: "one phrase missing: behaviour",
			plan: plan("`docs/design-system.md` wins on looks.\n", task("T1.1", "A", "—"))},
		{name: "one phrase missing: looks",
			plan: plan("The plan wins on behaviour over `docs/design-system.md`.\n", task("T1.1", "A", "—"))},
		{name: "the rule across a line break and in capitals",
			plan:     plan("Against `Docs/Design-System.md` the plan WINS ON\nbehaviour, and the design system wins   on\nlooks.\n", task("T1.1", "A", "—")),
			wantRule: true},
		{name: "the rule inside a fenced example heading still counts as header",
			plan:     plan("```\n## not a heading\n```\n"+rule, task("T1.1", "A", "—")),
			wantRule: true},
		{name: "a design task nobody waits for",
			plan:     plan(rule, task("T1.1", "A", "—"), design),
			wantRule: true, wantTask: "T1.2", wantFirst: true},
		{name: "two tasks wait, one names it twice over a note",
			plan: plan(rule, task("T1.1", "A", "—"), design,
				task("T1.3", "A page", "T1.1, T1.2"), task("T1.4", "Another page", "T1.2 (the tokens)")),
			wantRule: true, wantTask: "T1.2", wantFirst: true, wantDependents: 2},
		{name: "the task without the rule",
			plan:     plan("", task("T1.1", "A", "—"), design, task("T1.3", "A page", "T1.2")),
			wantTask: "T1.2", wantFirst: true, wantDependents: 1},
		{name: "an agent's design task",
			plan:     plan(rule, task("T1.1", "Design system (owner + agent)", "—"), task("T1.2", "A page", "T1.1")),
			wantRule: true},
		{name: "a design task for one area",
			plan:     plan(rule, task("T1.1", "Design system for the admin pages (owner)", "—"), task("T1.2", "A page", "T1.1")),
			wantRule: true},
		{name: "the task with a note in its bracket",
			plan:     plan(rule, task("T1.1", "design system (Owner; half a day)", "—"), task("T1.2", "A page", "T1.1")),
			wantRule: true, wantTask: "T1.1", wantFirst: true, wantDependents: 1},
		{name: "the task in a later milestone",
			plan: plan(rule, task("T1.1", "A", "—")) + "### M2 — Second\n\n" +
				task("T2.1", "Design system (owner)", "—") + task("T2.2", "A page", "T2.1"),
			wantRule: true, wantTask: "T2.1", wantDependents: 1},
		{name: "a plan that starts at M0",
			plan: "# Plan\n" + rule + "\n## Milestones\n\n### M0 — Set up\n\n" +
				task("T0.1", "A", "—") + task("T0.2", "Design system (owner)", "—") + task("T0.3", "A page", "T0.2"),
			wantRule: true, wantTask: "T0.2", wantFirst: true, wantDependents: 1},
		{name: "two design tasks, the first is the one",
			plan: plan(rule, design, task("T1.3", "A page", "T1.2")) + "### M2 — Second\n\n" +
				task("T2.1", "Design system (owner)", "—") + task("T2.2", "A page", "T2.1"),
			wantRule: true, wantTask: "T1.2", wantFirst: true, wantDependents: 1},
		{name: "a plan that does not parse",
			plan: plan(rule, design, task("T1.3", "A page", "T1.2")) + "#### T1.9 no dash\n"},
		{name: "a plan with no tasks", plan: "# Plan\n" + rule},
		{name: "an empty plan", plan: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule, task, first, dependents := designFacts([]byte(tt.plan))
			if rule != tt.wantRule || task != tt.wantTask || first != tt.wantFirst || dependents != tt.wantDependents {
				t.Errorf("designFacts = (rule %v, task %q, first %v, dependents %d), want (rule %v, task %q, first %v, dependents %d)",
					rule, task, first, dependents, tt.wantRule, tt.wantTask, tt.wantFirst, tt.wantDependents)
			}
		})
	}
}

// TestPlanDesignSession is the fixture session of a project with an
// interface: the real commands, the real checker and the design fixture of
// internal/planpatch, in the order the planning session runs them. The test
// writes the brief where the session would; no Go code writes it.
//
// Every step is load-bearing, since the steps are the phase as built:
//   - check goes on to design, not review, because the answer starts with yes;
//   - advance then refuses, names docs/design-brief.md and leaves state.json
//     byte for byte as it was, so the session cannot leave the phase by asking
//     twice;
//   - with the brief written the fixture plan passes the whole gate (the rule,
//     the task in the first milestone, tasks waiting for it, no checker error)
//     and the phase becomes review;
//   - status at review reports no problem and does not pull the session back.
func TestPlanDesignSession(t *testing.T) {
	dir := t.TempDir()
	plan := planFixture(t, "design.golden.md")
	if n := planlint.Lint([]byte(plan)).Summary.Errors; n != 0 {
		t.Fatalf("the design fixture has %d checker errors, want 0", n)
	}
	seedPlanSession(t, dir, plansession.PhaseCheck, "yes: a web page", plan)
	f := &planFake{}
	deps := realLintDeps(f, dir)
	advance := func() (int, string, string) {
		var so, se bytes.Buffer
		code := runPlanAdvance(nil, &so, &se, deps)
		return code, so.String(), se.String()
	}
	recorded := func() string {
		t.Helper()
		st, ok, err := plansession.ReadState(dir)
		if err != nil || !ok {
			t.Fatalf("state: ok=%v err=%v", ok, err)
		}
		return st.Phase
	}

	code, out, errOut := advance()
	if code != 0 || !strings.HasPrefix(out, "Phase: check → design\n") {
		t.Fatalf("first advance: exit %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	if got := recorded(); got != plansession.PhaseDesign {
		t.Fatalf("recorded phase = %q, want design", got)
	}

	before, _ := os.ReadFile(plansession.StatePath(dir))
	code, out, errOut = advance()
	if code != 1 || out != "" {
		t.Fatalf("advance without the brief: exit %d, want 1 and nothing on stdout\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	if !strings.Contains(errOut, plansession.DesignBriefFile) || !strings.Contains(errOut, "the phase was not changed") {
		t.Errorf("the refusal does not name the brief:\n%s", errOut)
	}
	if after, _ := os.ReadFile(plansession.StatePath(dir)); !bytes.Equal(before, after) {
		t.Errorf("state.json changed on a refused advance:\nbefore %q\nafter  %q", before, after)
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(plansession.DesignBriefFile))); err == nil {
		t.Errorf("a command wrote %s; only the session writes it", plansession.DesignBriefFile)
	}

	writePlanFile(t, dir, plansession.DesignBriefFile, "# Design brief\n\nA recipe box for one cook.\n")
	code, out, errOut = advance()
	if code != 0 || !strings.HasPrefix(out, "Phase: design → review\n") {
		t.Fatalf("advance with the brief: exit %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
	if got := recorded(); got != plansession.PhaseReview {
		t.Fatalf("recorded phase = %q, want review", got)
	}

	var so, se bytes.Buffer
	if code := runPlanStatus(nil, &so, &se, deps); code != 0 {
		t.Fatalf("status at review: exit %d\nstdout: %s\nstderr: %s", code, so.String(), se.String())
	}
	if !strings.Contains(so.String(), "Phase: review\n") || strings.Contains(so.String(), "Problem:") {
		t.Errorf("status at review:\n%s", so.String())
	}
	if len(f.calls) != 0 {
		t.Errorf("the commands made outside calls: %v", f.calls)
	}
}

// TestPlanDesignGate is `afk plan advance` at the design phase against plans
// that lack one thing each, with the real checker. Every refused row must
// exit 1 and leave state.json byte for byte as it was.
//
// Load-bearing rows:
//   - "base plan, brief written": the plan a session with an interface has
//     before the design phase did anything. The header rule is asked for
//     before the task, so that is what the refusal names.
//   - "base plan with the rule, brief written": says the plan has no design
//     task. This is the row that stops a session from writing the brief and
//     moving on without giving the owner the task.
//   - "nothing waits for the task": the design task alone is not enough; a
//     task that builds interface must wait for it, or the daemon would start
//     building pages while the owner is still designing.
//   - "plan with a checker error": the session is held at check, the earlier
//     phase, and told the count; the design gate is never reached with a plan
//     the checker rejects.
//   - "no interface, recorded at design": a session recorded at design whose
//     answer is no has no gate there and moves on without a brief.
func TestPlanDesignGate(t *testing.T) {
	design := planFixture(t, "design.golden.md")
	const brief = "# Design brief\n"
	tests := []struct {
		name            string
		answer          string
		plan            string
		brief           string
		wantCode        int
		wantOut         string
		wantErr         []string
		wantNoBriefFile bool
	}{
		{name: "no brief", answer: "yes: a web page", plan: design, wantCode: 1,
			wantErr: []string{"The design brief has not been written yet: docs/design-brief.md is missing or empty."}},
		{name: "a blank brief", answer: "yes: a web page", plan: design, brief: " \n", wantCode: 1,
			wantErr: []string{"docs/design-brief.md is missing or empty"}},
		{name: "base plan, brief written", answer: "yes: a web page", plan: planFixture(t, "base-plan.md"), brief: brief, wantCode: 1,
			wantErr: []string{"The plan's header does not say yet which document wins on what", "docs/design-system.md"}},
		{name: "base plan with the rule, brief written", answer: "yes: a web page", plan: basePlanWithRule(t), brief: brief, wantCode: 1,
			wantErr: []string{`The plan has no task titled "Design system (owner)" yet.`}},
		{name: "nothing waits for the task", answer: "yes: a web page", plan: strings.ReplaceAll(design, ", T1.4", ""), brief: brief, wantCode: 1,
			wantErr: []string{"No task waits for the design task T1.4 yet"}},
		{name: "plan with a checker error", answer: "yes: a web page",
			plan: strings.Replace(design, "- **Depends on:** T1.1, T1.4", "- **Depends on:** T1.1, T9.9", 1), brief: brief, wantCode: 1,
			wantErr: []string{"the earlier check phase is not finished", "The plan has 1 error that `afk plan lint` reports"}},
		{name: "everything there", answer: "yes: a web page", plan: design, brief: brief,
			wantOut: "Phase: design → review\n"},
		{name: "no interface, recorded at design", answer: "no: a command line", plan: planFixture(t, "base-plan.md"),
			wantOut: "Phase: design → review\n", wantNoBriefFile: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			seedPlanSession(t, dir, plansession.PhaseDesign, tt.answer, tt.plan)
			if tt.brief != "" {
				writePlanFile(t, dir, plansession.DesignBriefFile, tt.brief)
			}
			before, _ := os.ReadFile(plansession.StatePath(dir))
			f := &planFake{}
			var so, se bytes.Buffer
			code := runPlanAdvance(nil, &so, &se, realLintDeps(f, dir))
			if code != tt.wantCode {
				t.Fatalf("exit = %d, want %d\nstdout: %s\nstderr: %s", code, tt.wantCode, so.String(), se.String())
			}
			if !strings.HasPrefix(so.String(), tt.wantOut) || (tt.wantOut == "" && so.Len() > 0) {
				t.Errorf("stdout = %q, want it to start with %q", so.String(), tt.wantOut)
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(se.String(), want) {
					t.Errorf("stderr missing %q:\n%s", want, se.String())
				}
			}
			after, _ := os.ReadFile(plansession.StatePath(dir))
			if tt.wantCode != 0 && !bytes.Equal(before, after) {
				t.Errorf("state.json changed on a refused advance:\nbefore %q\nafter  %q", before, after)
			}
			if tt.wantNoBriefFile {
				if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(plansession.DesignBriefFile))); err == nil {
					t.Errorf("%s exists in a project with no interface", plansession.DesignBriefFile)
				}
			}
		})
	}
}

// TestPlanSessionWithoutAnInterface is the fixture session of a project with
// no interface: from check the session goes straight to review, and it ends
// with no brief, no design task and no extra dependency (the plan is the base
// fixture, byte for byte, with zero checker errors).
//
// Load-bearing: the exact `check → review` line. A session sent to design
// here would be recorded at a phase with nothing behind it.
func TestPlanSessionWithoutAnInterface(t *testing.T) {
	dir := t.TempDir()
	base := planFixture(t, "base-plan.md")
	if n := planlint.Lint([]byte(base)).Summary.Errors; n != 0 {
		t.Fatalf("the base fixture has %d checker errors, want 0", n)
	}
	seedPlanSession(t, dir, plansession.PhaseCheck, "no: a command line", base)
	f := &planFake{}
	deps := realLintDeps(f, dir)

	var so, se bytes.Buffer
	if code := runPlanAdvance(nil, &so, &se, deps); code != 0 || !strings.HasPrefix(so.String(), "Phase: check → review\n") {
		t.Fatalf("advance: exit %d\nstdout: %s\nstderr: %s", code, so.String(), se.String())
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(plansession.DesignBriefFile))); err == nil {
		t.Errorf("%s exists in a project with no interface", plansession.DesignBriefFile)
	}
	got, err := os.ReadFile(filepath.Join(dir, "docs", "PLAN.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != base {
		t.Errorf("the plan was changed by advance")
	}
	if rule, task, _, dependents := designFacts(got); rule || task != "" || dependents != 0 {
		t.Errorf("designFacts of the plan = (rule %v, task %q, dependents %d), want none", rule, task, dependents)
	}
	for _, task := range masterplan.Scan(got).Tasks {
		if task.Owner() {
			t.Errorf("the plan has an owner task, %s %q", task.ID, task.Title)
		}
	}

	so.Reset()
	se.Reset()
	if code := runPlanStatus(nil, &so, &se, deps); code != 0 || !strings.Contains(so.String(), "Phase: review\n") {
		t.Errorf("status: exit %d\nstdout: %s\nstderr: %s", code, so.String(), se.String())
	}
}

// TestPlanReconcileDesignFixture is the fixture of a design system that
// disagrees with the plan, run through the real commands: the plan the design
// phase left, the owner's design system, one `afk plan patch` where the
// design wins on looks, and the follow-up prompt for the point where the plan
// wins on behaviour.
//
// Load-bearing:
//   - the patched plan is byte-equal to reconcile-design.golden.md, the same
//     file internal/planpatch's own test ends at, so the command and the
//     package make one patch;
//   - T2.2's acceptance is untouched: the conflict on behaviour goes to the
//     owner in the follow-up prompt and is not settled in the plan;
//   - next-followup prints prompt 1 before the prompt is written and 2 after,
//     and writes nothing itself (the test writes the fixture prompt where the
//     session would).
func TestPlanReconcileDesignFixture(t *testing.T) {
	project, typedIn := t.TempDir(), t.TempDir()
	writePlanFile(t, project, "docs/PLAN.md", planFixture(t, "design.golden.md"))
	writePlanFile(t, project, masterplan.DesignSystemFile, planFixture(t, "design-system.md"))
	writePlanFile(t, typedIn, "old.md", "- **Scope:** The home page, sorted by name, with a link to each recipe.\n")
	writePlanFile(t, typedIn, "new.md", "- **Scope:** The home page, sorted by name, with a link to each recipe, each drawn as the `RecipeCard` of `docs/design-system.md`.\n")

	git := &patchGit{} // not a repository: the plan is changed in place
	deps := planPatchDeps{
		cwd:          typedIn,
		gitCommonDir: func(string) string { return "" },
		run:          git.run,
		tempDir:      func() (string, error) { return "", errors.New("no test here makes a worktree") },
	}
	run := func(cmd func([]string, *bytes.Buffer, *bytes.Buffer, planPatchDeps) int, args ...string) string {
		t.Helper()
		var so, se bytes.Buffer
		if code := cmd(append([]string{"--project", project}, args...), &so, &se, deps); code != 0 {
			t.Fatalf("%v: exit %d\nstdout: %s\nstderr: %s", args, code, so.String(), se.String())
		}
		return so.String()
	}
	patch := func(a []string, so, se *bytes.Buffer, d planPatchDeps) int { return runPlanPatch(a, so, se, d) }
	followup := func(a []string, so, se *bytes.Buffer, d planPatchDeps) int {
		return runPlanNextFollowup(a, so, se, d)
	}

	if out := run(patch, "--old", "old.md", "--new", "new.md"); !strings.Contains(out, "Tasks changed: T1.2.") {
		t.Errorf("patch said:\n%s", out)
	}
	got, err := os.ReadFile(filepath.Join(project, "docs", "PLAN.md"))
	if err != nil {
		t.Fatal(err)
	}
	if want := planFixture(t, "reconcile-design.golden.md"); string(got) != want {
		t.Errorf("the patched plan is not reconcile-design.golden.md:\n%s", got)
	}
	if n := planlint.Lint(got).Summary.Errors; n != 0 {
		t.Errorf("the reconciled plan has %d checker errors, want 0", n)
	}
	const t22 = "- **Acceptance:** The link opens the recipe in a private window, and a wrong token shows a not-found page."
	if !strings.Contains(planFixture(t, "base-plan.md"), t22) {
		t.Fatalf("the base fixture no longer has T2.2's acceptance as %q; fix this test's copy", t22)
	}
	if !strings.Contains(string(got), t22) {
		t.Errorf("T2.2's acceptance was changed; the plan wins on behaviour")
	}

	before := folderEntries(t, project)
	if out := run(followup); out != "docs/design-followup-1.md\n" {
		t.Errorf("next-followup = %q, want docs/design-followup-1.md", out)
	}
	if after := folderEntries(t, project); !reflect.DeepEqual(before, after) {
		t.Errorf("next-followup changed the project: before %v, after %v", before, after)
	}
	writePlanFile(t, project, "docs/design-followup-1.md", planFixture(t, "design-followup-1.md"))
	if out := run(followup); out != "docs/design-followup-2.md\n" {
		t.Errorf("next-followup after the first prompt = %q, want docs/design-followup-2.md", out)
	}
}
