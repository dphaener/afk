package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/host"
	"github.com/dphaener/afk/internal/session"
)

// TestNewAutonomousForSurface covers starting a session without a terminal.
// Load-bearing rows: "config is autonomous not unattended" (the page must
// start exactly what `afk new --autonomous --description D name` starts, and
// must never grant the daemon's permission mode); the refusal rows, each of
// which must stop before Build or Run is called; "reserved name" goes through
// the real session.Build, so the refusal is the one the command prints;
// "preflight failed" must stay a warning, not a refusal.
func TestNewAutonomousForSurface(t *testing.T) {
	tests := []struct {
		name        string
		sname       string
		description string
		check       string
		build       error
		run         error
		preflight   bool
		realBuild   bool
		wantOutcome string
		wantText    []string
		wantBuilds  int
		wantRuns    int
	}{
		{name: "config is autonomous not unattended", sname: "add-search", description: "Add search", preflight: true,
			wantOutcome: sessionActionDone, wantText: []string{"Started add-search", "afk dash"}, wantBuilds: 1, wantRuns: 1},
		{name: "blank description", sname: "add-search", description: "  \n ",
			wantOutcome: sessionActionRefused, wantText: []string{"description"}},
		{name: "description over 5000", sname: "add-search", description: strings.Repeat("x", 5001),
			wantOutcome: sessionActionRefused, wantText: []string{"too long", "5000"}},
		{name: "description of exactly 5000", sname: "add-search", description: strings.Repeat("x", 5000), preflight: true,
			wantOutcome: sessionActionDone, wantText: []string{"Started"}, wantBuilds: 1, wantRuns: 1},
		{name: "provider check refuses", sname: "add-search", description: "d", check: "afk new: codex needs --autonomous",
			wantOutcome: sessionActionRefused, wantText: []string{"codex needs --autonomous"}},
		{name: "reserved name", sname: "master-plan", description: "d", realBuild: true,
			wantOutcome: sessionActionRefused, wantText: []string{"master-plan", "afk plan"}, wantRuns: 0},
		{name: "bad name", sname: "Bad Name", description: "d", realBuild: true,
			wantOutcome: sessionActionRefused, wantText: []string{"Bad Name"}},
		{name: "existing branch", sname: "taken", description: "d", build: errors.New(`branch "taken" already exists`),
			wantOutcome: sessionActionRefused, wantText: []string{`branch "taken" already exists`}, wantBuilds: 1},
		{name: "run fails", sname: "add-search", description: "d", run: errors.New("worktree could not be created"), preflight: true,
			wantOutcome: sessionActionFailed, wantText: []string{"Starting add-search failed", "worktree could not be created"}, wantBuilds: 1, wantRuns: 1},
		{name: "preflight failed stays a warning", sname: "add-search", description: "d", preflight: false,
			wantOutcome: sessionActionDone, wantText: []string{"Started add-search", "Warning: install the plugin"}, wantBuilds: 1, wantRuns: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var builds, runs int
			var gotCfg session.Config
			d := newSurfaceDeps{
				CheckProvider: func(string, bool, bool) string { return tt.check },
				Build: func(cfg session.Config, _ session.Deps) (*session.Plan, error) {
					builds++
					gotCfg = cfg
					if tt.build != nil {
						return nil, tt.build
					}
					return &session.Plan{Config: cfg, PreflightOK: tt.preflight, PreflightMsg: "install the plugin"}, nil
				},
				Run: func(*session.Plan) error {
					runs++
					return tt.run
				},
			}
			if tt.realBuild {
				d.Build = func(cfg session.Config, sd session.Deps) (*session.Plan, error) {
					builds++
					return session.Build(cfg, sd)
				}
			}
			got := newAutonomousForSurface("/repo", tt.sname, tt.description, d)
			if got.Outcome != tt.wantOutcome {
				t.Fatalf("outcome = %q (%q), want %q", got.Outcome, got.Text, tt.wantOutcome)
			}
			for _, w := range tt.wantText {
				if !strings.Contains(got.Text, w) {
					t.Errorf("text %q is missing %q", got.Text, w)
				}
			}
			if strings.Contains(got.Text, "afk new:") || strings.HasPrefix(got.Text, "afk ") {
				t.Errorf("text %q carries a command prefix", got.Text)
			}
			if !tt.realBuild && builds != tt.wantBuilds {
				t.Errorf("Build calls = %d, want %d", builds, tt.wantBuilds)
			}
			if runs != tt.wantRuns {
				t.Errorf("Run calls = %d, want %d", runs, tt.wantRuns)
			}
			if tt.name == "config is autonomous not unattended" {
				want := session.Config{Name: "add-search", Description: "Add search", RepoDir: "/repo", Autonomous: true}
				if gotCfg != want {
					t.Errorf("config = %+v, want %+v", gotCfg, want)
				}
				if !gotCfg.Autonomous || gotCfg.Unattended {
					t.Errorf("Autonomous=%v Unattended=%v, want true and false", gotCfg.Autonomous, gotCfg.Unattended)
				}
			}
		})
	}
}

// restartRig records what a restart called.
type restartRig struct {
	rows       []discover.SessionState
	discErr    error
	spawnErr   error
	refused    string
	reqErr     error
	spawned    []session.HostSpawn
	requested  []string
	labelled   []string
	labelErr   error
	labelCalls int
}

func (r *restartRig) restartDeps() restartSurfaceDeps {
	return restartSurfaceDeps{
		Discover: func(string) ([]discover.SessionState, error) { return r.rows, r.discErr },
		SpawnHost: func(s session.HostSpawn) (session.HostProcess, error) {
			r.spawned = append(r.spawned, s)
			return session.HostProcess{}, r.spawnErr
		},
		RequestRestart: func(wt, name string) (string, error) {
			r.requested = append(r.requested, wt+"|"+name)
			return r.refused, r.reqErr
		},
	}
}

func (r *restartRig) labelDeps() labelSurfaceDeps {
	return labelSurfaceDeps{
		Discover: func(string) ([]discover.SessionState, error) { return r.rows, r.discErr },
		WriteLabel: func(wt, name, label string) error {
			r.labelCalls++
			r.labelled = append(r.labelled, wt+"|"+name+"|"+label)
			return r.labelErr
		},
	}
}

func row(name, wt string, mod func(*discover.SessionState)) discover.SessionState {
	s := discover.SessionState{Name: name, WorktreePath: wt}
	if mod != nil {
		mod(&s)
	}
	return s
}

// TestRestartSessionForSurface covers every outcome of a restart. Load-bearing
// rows: "healthy running host" (a restart drops the attached client, so a stale
// page must never do it to a session that answers), "one-off" and "planning
// session" (nothing to resume from; the planning session is afk plan's), and
// "stopped host" versus "wedged host" (two different actions, and only the
// second may send a restart request). No row may reach for Exit: the deps have
// no such seam.
func TestRestartSessionForSurface(t *testing.T) {
	stopped := row("feat", "/wt/feat", nil)
	wedged := row("feat", "/wt/feat", func(s *discover.SessionState) { s.HostRunning = true; s.WedgedSince = "2026-10-14T10:00:00Z" })
	tests := []struct {
		name        string
		rig         restartRig
		sname       string
		wantOutcome string
		wantText    string
		wantSpawn   int
		wantRequest int
	}{
		{name: "stopped host", rig: restartRig{rows: []discover.SessionState{stopped}}, sname: "feat",
			wantOutcome: sessionActionDone, wantText: "Started the host for feat", wantSpawn: 1},
		{name: "wedged host", rig: restartRig{rows: []discover.SessionState{wedged}}, sname: "feat",
			wantOutcome: sessionActionDone, wantText: "Restarted feat", wantRequest: 1},
		{name: "healthy running host", rig: restartRig{rows: []discover.SessionState{row("feat", "/wt/feat", func(s *discover.SessionState) { s.HostRunning = true })}}, sname: "feat",
			wantOutcome: sessionActionRefused, wantText: "running and answering"},
		{name: "stale wedged flag on a stopped host spawns", rig: restartRig{rows: []discover.SessionState{row("feat", "/wt/feat", func(s *discover.SessionState) { s.WedgedSince = "2026-10-14T10:00:00Z" })}}, sname: "feat",
			wantOutcome: sessionActionDone, wantText: "Started the host", wantSpawn: 1},
		{name: "one-off", rig: restartRig{rows: []discover.SessionState{row("scr", "/repo", func(s *discover.SessionState) { s.Scratch = true })}}, sname: "scr",
			wantOutcome: sessionActionRefused, wantText: "afk dash"},
		{name: "planning session", rig: restartRig{rows: []discover.SessionState{row("master-plan", "/repo", nil)}}, sname: "master-plan",
			wantOutcome: sessionActionRefused, wantText: "afk plan"},
		{name: "worktree no session", rig: restartRig{rows: []discover.SessionState{row("odd", "/wt/odd", func(s *discover.SessionState) { s.Kind = session.KindWorktreeNoSession })}}, sname: "odd",
			wantOutcome: sessionActionRefused, wantText: "problem with its worktree"},
		{name: "worktree missing", rig: restartRig{rows: []discover.SessionState{row("gone", "/wt/gone", func(s *discover.SessionState) { s.Kind = session.KindWorktreeMissing })}}, sname: "gone",
			wantOutcome: sessionActionRefused, wantText: "problem with its worktree"},
		{name: "unknown name", rig: restartRig{rows: []discover.SessionState{stopped}}, sname: "nope",
			wantOutcome: sessionActionRefused, wantText: "no session named nope"},
		{name: "discovery fails", rig: restartRig{discErr: errors.New("git broke")}, sname: "feat",
			wantOutcome: sessionActionFailed, wantText: "git broke"},
		{name: "spawn fails", rig: restartRig{rows: []discover.SessionState{stopped}, spawnErr: errors.New("no socket")}, sname: "feat",
			wantOutcome: sessionActionFailed, wantText: "host.log", wantSpawn: 1},
		{name: "host refuses", rig: restartRig{rows: []discover.SessionState{wedged}, refused: "no session id yet"}, sname: "feat",
			wantOutcome: sessionActionFailed, wantText: "refused to restart feat: no session id yet", wantRequest: 1},
		{name: "outcome unknown", rig: restartRig{rows: []discover.SessionState{wedged}, reqErr: host.ErrRestartUnknown}, sname: "feat",
			wantOutcome: sessionActionFailed, wantText: "did not say how it went", wantRequest: 1},
		{name: "request error", rig: restartRig{rows: []discover.SessionState{wedged}, reqErr: errors.New("dial failed")}, sname: "feat",
			wantOutcome: sessionActionFailed, wantText: "dial failed", wantRequest: 1},
		{name: "first row with the name wins", rig: restartRig{rows: []discover.SessionState{stopped, row("feat", "/repo", func(s *discover.SessionState) { s.Scratch = true })}}, sname: "feat",
			wantOutcome: sessionActionDone, wantText: "Started the host", wantSpawn: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rig := tt.rig
			got := restartSessionForSurface("/repo", tt.sname, rig.restartDeps())
			if got.Outcome != tt.wantOutcome || !strings.Contains(got.Text, tt.wantText) {
				t.Fatalf("got %+v, want outcome %q containing %q", got, tt.wantOutcome, tt.wantText)
			}
			if len(rig.spawned) != tt.wantSpawn || len(rig.requested) != tt.wantRequest {
				t.Errorf("spawns=%d requests=%d, want %d and %d", len(rig.spawned), len(rig.requested), tt.wantSpawn, tt.wantRequest)
			}
			if tt.wantSpawn == 1 {
				if s := rig.spawned[0]; s.Name != "feat" || s.Worktree != "/wt/feat" || s.Scratch || s.MasterPlan {
					t.Errorf("spawn = %+v, want the discovered worktree and no scratch or planning mode", s)
				}
			}
			if tt.wantRequest == 1 && rig.requested[0] != "/wt/feat|feat" {
				t.Errorf("request = %q, want the discovered worktree", rig.requested[0])
			}
		})
	}
}

// TestSetLabelForSurface covers the label write. Load-bearing rows: "129
// characters" (refused before discovery, nothing written), "worktree comes
// from discovery" (the caller never names it), "worktree fault" (nowhere right
// to write), and "blank clears" (the sidecar is removed by WriteLabel, so the
// call still happens).
func TestSetLabelForSurface(t *testing.T) {
	ok := row("feat", "/wt/feat", nil)
	tests := []struct {
		name        string
		rig         restartRig
		label       string
		wantOutcome string
		wantText    string
		wantWrite   string
	}{
		{name: "set", rig: restartRig{rows: []discover.SessionState{ok}}, label: "billing",
			wantOutcome: sessionActionDone, wantText: `Labelled feat "billing"`, wantWrite: "/wt/feat|feat|billing"},
		{name: "worktree comes from discovery", rig: restartRig{rows: []discover.SessionState{row("feat", "/elsewhere/wt", nil)}}, label: "x",
			wantOutcome: sessionActionDone, wantText: "Labelled", wantWrite: "/elsewhere/wt|feat|x"},
		{name: "blank clears", rig: restartRig{rows: []discover.SessionState{ok}}, label: "   ",
			wantOutcome: sessionActionDone, wantText: "Cleared the label of feat", wantWrite: "/wt/feat|feat|"},
		{name: "128 characters", rig: restartRig{rows: []discover.SessionState{ok}}, label: strings.Repeat("é", 128),
			wantOutcome: sessionActionDone, wantText: "Labelled", wantWrite: "/wt/feat|feat|" + strings.Repeat("é", 128)},
		{name: "129 characters", rig: restartRig{rows: []discover.SessionState{ok}}, label: strings.Repeat("x", 129),
			wantOutcome: sessionActionRefused, wantText: "too long"},
		{name: "unknown name", rig: restartRig{rows: []discover.SessionState{row("other", "/wt/o", nil)}}, label: "x",
			wantOutcome: sessionActionRefused, wantText: "no session named feat"},
		{name: "worktree fault", rig: restartRig{rows: []discover.SessionState{row("feat", "/wt/feat", func(s *discover.SessionState) { s.Kind = session.KindWorktreeNoSession })}}, label: "x",
			wantOutcome: sessionActionRefused, wantText: "problem with its worktree"},
		{name: "discovery fails", rig: restartRig{discErr: errors.New("git broke")}, label: "x",
			wantOutcome: sessionActionFailed, wantText: "git broke"},
		{name: "write fails", rig: restartRig{rows: []discover.SessionState{ok}, labelErr: errors.New("disk full")}, label: "x",
			wantOutcome: sessionActionFailed, wantText: "disk full", wantWrite: "/wt/feat|feat|x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rig := tt.rig
			got := setLabelForSurface("/repo", "feat", tt.label, rig.labelDeps())
			if got.Outcome != tt.wantOutcome || !strings.Contains(got.Text, tt.wantText) {
				t.Fatalf("got %+v, want outcome %q containing %q", got, tt.wantOutcome, tt.wantText)
			}
			if tt.wantWrite == "" {
				if rig.labelCalls != 0 {
					t.Errorf("WriteLabel called %d times, want none", rig.labelCalls)
				}
				return
			}
			if len(rig.labelled) != 1 || rig.labelled[0] != tt.wantWrite {
				t.Errorf("writes = %q, want [%q]", rig.labelled, tt.wantWrite)
			}
		})
	}
}
