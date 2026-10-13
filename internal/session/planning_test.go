package session

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/provider"
)

// TestPlanningMarker is the marker's round trip and its reader's edges.
// Load-bearing rows: "written marker is read back" ties the writer and the
// reader to one path; "missing marker" is every session that is not a
// planning session and must stay off, or an ordinary session's relaunch would
// start the planning skill; "another session's marker" proves the marker is
// per session, so a planning session in a project does not turn its
// neighbours into planning sessions.
func TestPlanningMarker(t *testing.T) {
	const name = PlanSessionName
	tests := []struct {
		name  string
		mkdir bool
		setup func(t *testing.T, worktree, dir string)
		want  bool
	}{
		{
			name:  "written marker is read back",
			mkdir: true,
			setup: func(t *testing.T, _, dir string) {
				if err := WritePlanningMarker(dir); err != nil {
					t.Fatalf("WritePlanningMarker: %v", err)
				}
				info, err := os.Stat(filepath.Join(dir, "planning"))
				if err != nil {
					t.Fatalf("marker is not the file `planning` in the session folder: %v", err)
				}
				if info.Size() != 0 {
					t.Errorf("marker size = %d, want an empty file", info.Size())
				}
			},
			want: true,
		},
		{
			name:  "writing twice is not an error",
			mkdir: true,
			setup: func(t *testing.T, _, dir string) {
				for i := 0; i < 2; i++ {
					if err := WritePlanningMarker(dir); err != nil {
						t.Fatalf("WritePlanningMarker #%d: %v", i+1, err)
					}
				}
			},
			want: true,
		},
		{
			name:  "missing marker",
			mkdir: true,
			setup: func(*testing.T, string, string) {},
			want:  false,
		},
		{
			name:  "missing session folder",
			setup: func(*testing.T, string, string) {},
			want:  false,
		},
		{
			name:  "another session's marker",
			mkdir: true,
			setup: func(t *testing.T, worktree, _ string) {
				other := filepath.Join(worktree, ".afk", "billing")
				if err := os.MkdirAll(other, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := WritePlanningMarker(other); err != nil {
					t.Fatal(err)
				}
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			worktree := t.TempDir()
			dir := filepath.Join(worktree, ".afk", name)
			if tt.mkdir {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			tt.setup(t, worktree, dir)
			if got := IsPlanning(worktree, name); got != tt.want {
				t.Errorf("IsPlanning = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestWritePlanningMarkerNeedsTheFolder: the writer does not create the
// session folder, as the other marker writers do not, so a caller that forgot
// to make it hears about it instead of getting a marker somewhere else.
func TestWritePlanningMarkerNeedsTheFolder(t *testing.T) {
	if err := WritePlanningMarker(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("WritePlanningMarker into a missing folder returned no error")
	}
}

// TestReservedName covers the two reserved names and their near misses.
// Load-bearing rows: "plan" (its folder is the planning state, which cleaning
// a one-off session would delete) and "master-plan" (the planning session's
// own host folder and socket). The near-miss rows keep the rule exact: a
// session called `plan-b` or `planner` is an ordinary session and stays
// allowed, and so does every name that was valid before.
func TestReservedName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"plan", "plan", true},
		{"master-plan", "master-plan", true},
		{"the constant", PlanSessionName, true},
		{"plans", "plans", false},
		{"planner", "planner", false},
		{"plan with a suffix", "plan-b", false},
		{"master-plan with a suffix", "master-plan-2", false},
		{"master", "master", false},
		{"ordinary", "billing", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ReservedName(tt.in); got != tt.want {
				t.Errorf("ReservedName(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
	// ValidateName is used by `afk plan check` for plan names and must keep
	// accepting both: the reservation is Build's, not the name rule's.
	for _, n := range []string{"plan", PlanSessionName} {
		if err := ValidateName(n); err != nil {
			t.Errorf("ValidateName(%q) = %v; reserving the name must not change the name rule", n, err)
		}
	}
}

// TestResolveSpawnPlanning covers how a spawn becomes a planning session and
// every way that is refused. It runs the three resolve steps in SpawnHost's
// order, because the unattended marker is only read by the last one.
// Load-bearing rows: "marker, field clear" is the dashboard's relaunch of a
// stopped host, which builds a HostSpawn with only Name and Worktree; without
// it a relaunched planning session would start a conductor. "no marker, field
// clear" is every other session and must come out untouched. The refusal rows
// must each be an error and never a launch: "with the unattended field" and
// "with the unattended marker" would leave the session waiting on a question
// nobody sees, "with a resume id" is the daemon's way of bringing a session
// back unattended, and the two "provider with no planning session" rows would
// otherwise start a host with no child command.
func TestResolveSpawnPlanning(t *testing.T) {
	without := providerWithoutPlan(t)
	const name = PlanSessionName
	cases := []struct {
		name             string
		planningMarker   bool
		unattendedMarker bool
		spec             HostSpawn
		want             bool
		wantErr          error
	}{
		{name: "no marker, field clear", want: false},
		{name: "marker, field clear", planningMarker: true, want: true},
		{name: "no marker, field set", spec: HostSpawn{MasterPlan: true}, want: true},
		{name: "marker and field", planningMarker: true, spec: HostSpawn{MasterPlan: true}, want: true},
		{name: "provider with no planning session, not planning", spec: HostSpawn{Provider: without}, want: false},
		{name: "provider with no planning session, field set", spec: HostSpawn{Provider: without, MasterPlan: true}, wantErr: provider.ErrNoPlanSession},
		{name: "provider with no planning session, marker", planningMarker: true, spec: HostSpawn{Provider: without}, wantErr: provider.ErrNoPlanSession},
		{name: "with the unattended field", spec: HostSpawn{MasterPlan: true, Unattended: true}, wantErr: ErrPlanningUnattended},
		{name: "marker, with the unattended field", planningMarker: true, spec: HostSpawn{Unattended: true}, wantErr: ErrPlanningUnattended},
		{name: "with the unattended marker", unattendedMarker: true, spec: HostSpawn{MasterPlan: true}, wantErr: ErrPlanningUnattended},
		{name: "both markers", planningMarker: true, unattendedMarker: true, wantErr: ErrPlanningUnattended},
		{name: "with a resume id", spec: HostSpawn{MasterPlan: true, ResumeID: "0f3c9a2e"}, wantErr: ErrPlanningUnattended},
		{name: "marker, with a resume id", planningMarker: true, spec: HostSpawn{ResumeID: "0f3c9a2e"}, wantErr: ErrPlanningUnattended},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			wt := t.TempDir()
			dir := filepath.Join(wt, ".afk", name)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if tt.planningMarker {
				if err := WritePlanningMarker(dir); err != nil {
					t.Fatal(err)
				}
			}
			if tt.unattendedMarker {
				if err := WriteUnattendedMarker(dir); err != nil {
					t.Fatal(err)
				}
			}
			spec := tt.spec
			spec.Name, spec.Worktree = name, wt
			got, err := resolveSpawnProvider(spec)
			if err == nil {
				got, err = resolveSpawnPlanning(got)
			}
			if err == nil {
				got, err = resolveSpawnUnattended(got)
			}
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("resolved to %+v, err %v; want %v", got, err, tt.wantErr)
				}
				t.Logf("refused with: %v", err)
				if !strings.Contains(err.Error(), name) {
					t.Errorf("error %q does not name the session", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if got.MasterPlan != tt.want {
				t.Fatalf("MasterPlan = %v, want %v", got.MasterPlan, tt.want)
			}
			child := childOf(t, hostArgs(got, "/sock"))
			t.Logf("child argv: %q", child)
			p, err := provider.Lookup(spec.Provider)
			if err != nil {
				t.Fatal(err)
			}
			want := p.LaunchArgv(name, false, false)
			if tt.want {
				want = planLaunch(t)
			}
			if !reflect.DeepEqual(child, want) {
				t.Errorf("child argv = %#v\nwant %#v", child, want)
			}
		})
	}
}

// TestLaunchArgvPlanningUnattendedRefused pins the pure refusal on its own,
// with no files: a MasterPlan spec with Unattended or ResumeID is an error
// with a nil argv under every provider, so no caller that ignores the error
// (childArgv does) can be left holding a command to run. The message is shown
// to the owner, so it must say the session is never unattended.
func TestLaunchArgvPlanningUnattendedRefused(t *testing.T) {
	cases := []struct {
		name string
		spec HostSpawn
	}{
		{"unattended", HostSpawn{MasterPlan: true, Unattended: true}},
		{"resume id", HostSpawn{MasterPlan: true, ResumeID: "0f3c9a2e"}},
		{"both", HostSpawn{MasterPlan: true, Unattended: true, ResumeID: "0f3c9a2e"}},
	}
	for _, tt := range cases {
		for _, prov := range append([]string{""}, provider.Names()...) {
			t.Run(tt.name+"/provider="+prov, func(t *testing.T) {
				spec := tt.spec
				spec.Name, spec.Worktree, spec.Provider = PlanSessionName, "/repo", prov
				argv, err := launchArgv(spec)
				if !errors.Is(err, ErrPlanningUnattended) {
					t.Fatalf("launchArgv = %q, err %v; want ErrPlanningUnattended", argv, err)
				}
				if argv != nil {
					t.Errorf("launchArgv returned argv %q with the refusal; want nil", argv)
				}
				if !strings.Contains(err.Error(), "a planning session is never unattended") {
					t.Errorf("error %q does not say a planning session is never unattended", err)
				}
				if line := ChildCommandLine(spec); line != "" {
					t.Errorf("ChildCommandLine = %q for a refused spec, want \"\"", line)
				}
			})
		}
	}
}

// TestSpawnHostCarriesThePlanningMarker runs the dashboard's relaunch for
// real: SpawnHost is given only Name and Worktree, the stand-in for the afk
// binary records the argv it was started with, and the child argv in it must
// be the provider's planning command when the marker exists and the ordinary
// launch when it does not. TestResolveSpawnPlanning proves the resolution;
// this proves SpawnHost actually runs it before building the host command.
func TestSpawnHostCarriesThePlanningMarker(t *testing.T) {
	plain, err := provider.Lookup("")
	if err != nil {
		t.Fatal(err)
	}
	const name = PlanSessionName
	for _, tt := range []struct {
		name   string
		marker bool
	}{
		{"marker", true},
		{"no marker", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			shortTmpDir(t)
			wt := t.TempDir()
			dir := filepath.Join(wt, ".afk", name)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if tt.marker {
				if err := WritePlanningMarker(dir); err != nil {
					t.Fatal(err)
				}
			}
			// Written to a temp name and renamed, so the file is whole the
			// moment it appears.
			record := filepath.Join(t.TempDir(), "argv")
			script := filepath.Join(t.TempDir(), "fake-afk")
			body := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > '" + record + ".tmp'\nmv '" + record + ".tmp' '" + record + "'\n"
			if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}
			d := Deps{Executable: func() (string, error) { return script, nil }}
			if _, err := SpawnHost(HostSpawn{Name: name, Worktree: wt}, d); err != nil {
				t.Fatalf("SpawnHost: %v", err)
			}
			var raw []byte
			deadline := time.Now().Add(10 * time.Second)
			for {
				raw, err = os.ReadFile(record)
				if err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("the stand-in host never recorded its argv: %v", err)
				}
				time.Sleep(5 * time.Millisecond)
			}
			child := childOf(t, strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n"))
			t.Logf("child argv handed to the host: %q", child)
			want := plain.LaunchArgv(name, false, false)
			if tt.marker {
				want = planLaunch(t)
			}
			if !reflect.DeepEqual(child, want) {
				t.Fatalf("child argv = %#v\nwant %#v", child, want)
			}
		})
	}
}

// TestSpawnHostRefusesPlanning: every refused planning spawn fails before the
// afk binary is even located, so no host process is started. Load-bearing
// rows: "provider with no planning session" on a relaunch (provider and
// planning both read from their sidecars) is the row that would otherwise
// start a host with an empty child command, and "MasterPlan with Unattended"
// is the one that would leave a session waiting on a question nobody sees.
func TestSpawnHostRefusesPlanning(t *testing.T) {
	without := providerWithoutPlan(t)
	const name = PlanSessionName
	cases := []struct {
		name    string
		setup   func(t *testing.T, dir string, spec *HostSpawn)
		wantErr error
	}{
		{
			name: "provider with no planning session, relaunch",
			setup: func(t *testing.T, dir string, _ *HostSpawn) {
				if err := WriteProviderMarker(dir, without); err != nil {
					t.Fatal(err)
				}
				if err := WritePlanningMarker(dir); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: provider.ErrNoPlanSession,
		},
		{
			name: "provider with no planning session, asked outright",
			setup: func(_ *testing.T, _ string, spec *HostSpawn) {
				spec.Provider = without
				spec.MasterPlan = true
			},
			wantErr: provider.ErrNoPlanSession,
		},
		{
			name: "MasterPlan with Unattended",
			setup: func(_ *testing.T, _ string, spec *HostSpawn) {
				spec.MasterPlan = true
				spec.Unattended = true
			},
			wantErr: ErrPlanningUnattended,
		},
		{
			name: "planning marker with the unattended marker",
			setup: func(t *testing.T, dir string, _ *HostSpawn) {
				if err := WritePlanningMarker(dir); err != nil {
					t.Fatal(err)
				}
				if err := WriteUnattendedMarker(dir); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: ErrPlanningUnattended,
		},
		{
			name: "planning marker with a resume id",
			setup: func(t *testing.T, dir string, spec *HostSpawn) {
				if err := WritePlanningMarker(dir); err != nil {
					t.Fatal(err)
				}
				spec.ResumeID = "0f3c9a2e"
			},
			wantErr: ErrPlanningUnattended,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			wt := t.TempDir()
			dir := filepath.Join(wt, ".afk", name)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			spec := HostSpawn{Name: name, Worktree: wt}
			tt.setup(t, dir, &spec)
			called := 0
			d := Deps{Executable: func() (string, error) { called++; return exec.LookPath("true") }}
			h, err := SpawnHost(spec, d)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("SpawnHost = %+v, err %v; want %v", h, err, tt.wantErr)
			}
			t.Logf("SpawnHost refused with: %v", err)
			if !strings.Contains(err.Error(), name) {
				t.Errorf("error %q does not name the session", err)
			}
			if called != 0 {
				t.Errorf("Executable called %d times; the refusal must come before anything is forked", called)
			}
			if h.PID != 0 {
				t.Errorf("a host process was started (pid %d)", h.PID)
			}
			if _, statErr := os.Stat(filepath.Join(dir, "host.json")); statErr == nil {
				t.Error("host.json exists after a refused spawn")
			}
		})
	}
}

// TestBuildRefusesReservedNames: `afk new` may not take a name kept for
// `afk plan`, in any mode. Load-bearing rows: "plan, one-off" is the case that
// loses data, because a one-off session lives in .afk/plan/ itself and
// cleaning it removes that folder with the owner's answers in it; "ordinary"
// and "dry run" rows show the refusal is not tied to the one-off path and
// that a dry run cannot promise a session it would refuse. Every refusal must
// come before any command runs and before anything is written. "planner" is
// the control: a near miss builds, so the rows above fail for the name and
// not for the fixture.
func TestBuildRefusesReservedNames(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		refused bool
	}{
		{name: "plan, ordinary", cfg: Config{Name: "plan"}, refused: true},
		{name: "plan, one-off", cfg: Config{Name: "plan", Scratch: true}, refused: true},
		{name: "plan, dry run", cfg: Config{Name: "plan", DryRun: true}, refused: true},
		{name: "plan, one-off dry run", cfg: Config{Name: "plan", Scratch: true, DryRun: true}, refused: true},
		{name: "master-plan, ordinary", cfg: Config{Name: PlanSessionName}, refused: true},
		{name: "master-plan, one-off", cfg: Config{Name: PlanSessionName, Scratch: true}, refused: true},
		{name: "master-plan, autonomous", cfg: Config{Name: PlanSessionName, Autonomous: true}, refused: true},
		{name: "master-plan, unattended", cfg: Config{Name: PlanSessionName, Unattended: true}, refused: true},
		{name: "planner, ordinary", cfg: Config{Name: "planner"}},
		{name: "planner, one-off", cfg: Config{Name: "planner", Scratch: true}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			fr := &fakeRunner{}
			d := baseDeps(t, fr)
			cfg := tt.cfg
			cfg.RepoDir = repo
			p, err := Build(cfg, d)
			if !tt.refused {
				if err != nil {
					t.Fatalf("Build refused %q: %v", cfg.Name, err)
				}
				if p == nil {
					t.Fatal("Build returned no plan")
				}
				return
			}
			if err == nil {
				t.Fatalf("Build accepted the reserved name %q", cfg.Name)
			}
			t.Logf("Build refused with: %v", err)
			for _, want := range []string{cfg.Name, "afk plan", "another name"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
			if len(fr.calls) != 0 {
				t.Errorf("Build ran %d commands before refusing a reserved name; want 0", len(fr.calls))
			}
			entries, readErr := os.ReadDir(repo)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(entries) != 0 {
				t.Errorf("Build wrote %d entries into the repo before refusing; want none", len(entries))
			}
		})
	}
}
