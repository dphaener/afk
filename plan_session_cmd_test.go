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
// parser); "unknown word" (a mistyped subcommand must not start a session).
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
		{"lint", []string{"lint", "docs/PLAN.md"}, planRouteLint},
		{"check", []string{"check", "s", "W1.T1", "done"}, planRouteCheck},
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
//   - "plan and no session, dry run": the refusal comes before the dry run, so
//     a dry run never promises a start the real command would refuse.
//   - "no terminal": nothing is started (not start, not attach) when nobody
//     can answer; and "no terminal, host alive" shows it beats attach too.
//   - "provider refuses, dry run" and "damaged state, dry run": the other two
//     refusals also come before the dry run.
//   - "dry run without a terminal": the dry run still answers, since it asks
//     nothing.
//   - "session at draft with a plan": a session in progress is resumed even
//     though the plan exists; only a plan with no session is refused.
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
			planStartAction{Kind: planRefuseHasPlan}},
		{"plan and no session, dry run", with(func(in *planStartInput) { in.PlanExists = true; in.DryRun = true }),
			planStartAction{Kind: planRefuseHasPlan}},
		{"plan and no session, host alive", with(func(in *planStartInput) { in.PlanExists = true; in.HostAlive = true }),
			planStartAction{Kind: planRefuseHasPlan}},
		{"session approved", with(func(in *planStartInput) {
			in.HasState = true
			in.Recorded = plansession.PhaseApproved
			in.PlanExists = true
		}), planStartAction{Kind: planRefuseHasPlan}},
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
// is written); "plan and no session" (refused with the plan's path);
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
			fake: planFake{tty: true}, repo: true,
			wantCode: 1, wantErr: "already has a plan at " + filepath.Join("{dir}", "docs", "PLAN.md"),
		},
		{
			name: "plan named in afk.toml and no session",
			setup: func(t *testing.T, dir string) {
				writePlanFile(t, dir, "afk.toml", "plan = \"notes/build.md\"\n")
				writePlanFile(t, dir, "notes/build.md", "# Plan\n")
			},
			fake: planFake{tty: true}, repo: true, args: []string{"--dry-run"},
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
				writePlanFile(t, dir, ".afk/plan/questions.jsonl",
					`{"id":"q1","round":1,"kind":"setup","question":"Which domain?","answer":"example.com"}`+"\n")
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
				writePlanFile(t, dir, ".afk/plan/questions.jsonl",
					`{"id":"q1","round":1,"kind":"setup","question":"Which domain?","answer":"example.com"}`+"\n")
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
