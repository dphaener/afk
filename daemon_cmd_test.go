package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/daemon"
	"github.com/dphaener/afk/internal/daemonconfig"
	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/notify"
	"github.com/dphaener/afk/internal/projectconfig"
	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/usagestate"
)

var daemonTestNow = time.Date(2026, 10, 11, 9, 30, 0, 0, time.UTC)

const daemonTestPlan = "#### T1.1 — First\n\n- **Depends on:** none\n\n#### T1.2 — Second\n\n- **Depends on:** T1.1\n"

// daemonRig is one `afk daemon` invocation as the tests see it: temp config
// and state folders reached through the two Deps structs, and a fake loop
// underneath that records what it was asked to launch. Nothing in it reads
// the process's environment, the real home or git, and Launch never creates
// a session.
type daemonRig struct {
	t         *testing.T
	cfgHome   string
	stateHome string
	launches  []daemon.LaunchRequest
	providers []string
	alive     bool
	ctx       context.Context
	// common maps a folder to the git common dir the fake git reports.
	common map[string]string
}

func newDaemonRig(t *testing.T) *daemonRig {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // a daemon that is asked to stop the moment it starts
	return &daemonRig{t: t, cfgHome: t.TempDir(), stateHome: t.TempDir(), ctx: ctx, common: map[string]string{}}
}

func (r *daemonRig) writeConfig(content string) {
	r.t.Helper()
	writeBuildFile(r.t, r.cfgHome, "afk/config.toml", content)
}

func (r *daemonRig) configPath() string { return filepath.Join(r.cfgHome, "afk", "config.toml") }
func (r *daemonRig) stateDir() string   { return filepath.Join(r.stateHome, "afk") }

// project makes a folder the loop accepts as a checkout (it only looks for a
// .git entry) holding a two-task master plan.
func (r *daemonRig) project() string {
	r.t.Helper()
	root := r.t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		r.t.Fatal(err)
	}
	writeBuildFile(r.t, root, "docs/PLAN.md", daemonTestPlan)
	return root
}

func (r *daemonRig) env() daemonEnv {
	getenv := func(k string) string {
		switch k {
		case "XDG_CONFIG_HOME":
			return r.cfgHome
		case "XDG_STATE_HOME":
			return r.stateHome
		}
		return ""
	}
	noHome := func() (string, error) { return "", errors.New("no home in tests") }
	return daemonEnv{
		config:       daemonconfig.Deps{Getenv: getenv, HomeDir: noHome},
		state:        usagestate.Deps{Getenv: getenv, HomeDir: noHome},
		now:          func() time.Time { return daemonTestNow },
		loc:          time.UTC,
		pid:          4242,
		gitCommonDir: func(dir string) string { return r.common[dir] },
		isDir: func(p string) bool {
			fi, err := os.Stat(p)
			return err == nil && fi.IsDir()
		},
		alive: func(int) bool { return r.alive },
		newDeps: func(p provider.Provider, providerName string, log func(string, ...any)) daemon.Deps {
			r.providers = append(r.providers, providerName)
			return daemon.Deps{
				Now: func() time.Time { return daemonTestNow },
				LoadBuild: func(root string) ([]masterplan.Task, string, ledger.Ledger, error) {
					bs, err := loadBuildState(root)
					return bs.Tasks, bs.PlanPath, bs.Ledger, err
				},
				Sessions:  func(string) ([]discover.SessionState, error) { return nil, nil },
				Preflight: func(daemon.Project) (bool, string) { return true, "" },
				Launch: func(req daemon.LaunchRequest) error {
					r.launches = append(r.launches, req)
					return nil
				},
				RecordTokens: func(daemon.Project, string, string, string) (int, error) { return 0, nil },
				Log:          log,
			}
		},
		signalContext: func() (context.Context, context.CancelFunc) { return r.ctx, func() {} },
	}
}

// TestRunDaemon runs the command body against temp folders and a fake loop.
//
// Load-bearing rows:
//   - "no projects" and "status, no projects" differ only in the exit code:
//     the daemon cannot work with nothing to build (1), while status is a
//     read-only report and an empty one is not a failure (0).
//   - "paused project starts nothing" is the manual check's safety property:
//     it is what makes `afk daemon --once` safe to try on a real project.
//   - "already running" must not launch: two loops over one ledger would each
//     believe it runs one task at a time.
//   - "worktree path" pins that a project path naming a session's worktree
//     is run from the main checkout, where the ledger and the inbox live.
//   - "stops when asked" pins exit 0 on a stop signal; a service manager
//     reads anything else as a crash and restarts the daemon.
func TestRunDaemon(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		setup func(r *daemonRig)
		code  int
		// out and errOut are words stdout and stderr must hold.
		out, errOut []string
		launches    int
		check       func(t *testing.T, r *daemonRig)
	}{
		{
			name:   "no projects",
			args:   []string{"--once"},
			code:   1,
			errOut: []string{"no projects are configured", "[[project]]", "path", "config.toml"},
		},
		{
			name: "status, no projects",
			args: []string{"status"},
			code: 0,
			out:  []string{"No projects are configured", "[[project]]", "config.toml", "not running"},
		},
		{
			name: "unknown provider names the choices",
			args: []string{"--once"},
			setup: func(r *daemonRig) {
				r.writeConfig("provider = \"no-such-agent\"\n[[project]]\npath = \"" + r.project() + "\"\n")
			},
			code:   1,
			errOut: append([]string{`"no-such-agent"`, "config.toml"}, provider.Names()...),
		},
		{
			name:   "a tick that is not a duration",
			args:   []string{"--once"},
			setup:  func(r *daemonRig) { r.writeConfig("[daemon]\ntick = \"banana\"\n") },
			code:   1,
			errOut: []string{"config.toml", "tick", "banana"},
		},
		{
			name:   "unknown word",
			args:   []string{"bogus"},
			code:   2,
			errOut: []string{`"bogus"`, "Usage: " + daemonUsageLine},
		},
		{
			name:   "status with a second word",
			args:   []string{"status", "demo"},
			code:   2,
			errOut: []string{"Usage: " + daemonUsageLine},
		},
		{
			name:   "status with --once",
			args:   []string{"status", "--once"},
			code:   2,
			errOut: []string{"--once does not go with status"},
		},
		{
			name:   "unknown flag",
			args:   []string{"--forever"},
			code:   2,
			errOut: []string{"flag provided but not defined", "afk daemon --once"},
		},
		{
			name:   "help",
			args:   []string{"--help"},
			code:   0,
			errOut: []string{"afk daemon --once", "afk daemon status", "Exit codes"},
		},
		{
			name: "once starts the first task",
			args: []string{"--once"},
			setup: func(r *daemonRig) {
				r.writeConfig("[[project]]\nname = \"demo\"\npath = \"" + r.project() + "\"\n")
			},
			code:     0,
			out:      []string{"demo: started a task (T1.1, session t1-1)"},
			launches: 1,
			check: func(t *testing.T, r *daemonRig) {
				if got := r.launches[0]; got.Task != "T1.1" || got.Session != "t1-1" || got.Project.Name != "demo" {
					t.Errorf("launch = %+v, want T1.1 in t1-1 for demo", got)
				}
				if pid, _, ok := daemonstate.ReadMachine(r.stateDir()); !ok || pid != 4242 {
					t.Errorf("machine file = pid %d ok %v, want pid 4242", pid, ok)
				}
				if len(r.providers) != 1 || r.providers[0] != "" {
					t.Errorf("provider names handed to the loop = %q, want one empty (default) name", r.providers)
				}
			},
		},
		{
			name: "once written with a value",
			args: []string{"--once=true"},
			setup: func(r *daemonRig) {
				r.writeConfig("[[project]]\nname = \"demo\"\npath = \"" + r.project() + "\"\n")
			},
			code:     0,
			launches: 1,
		},
		{
			name: "paused project starts nothing",
			args: []string{"--once"},
			setup: func(r *daemonRig) {
				root := r.project()
				if err := daemonstate.Pause(root, daemonTestNow); err != nil {
					r.t.Fatal(err)
				}
				r.writeConfig("[[project]]\nname = \"demo\"\npath = \"" + root + "\"\n")
			},
			code: 0,
			out:  []string{"demo: waiting", "paused"},
		},
		{
			name: "already running",
			args: []string{"--once"},
			setup: func(r *daemonRig) {
				r.writeConfig("[[project]]\npath = \"" + r.project() + "\"\n")
				unlock, err := daemonstate.Lock(r.stateDir(), 777, daemonTestNow)
				if err != nil {
					r.t.Fatal(err)
				}
				r.t.Cleanup(unlock)
			},
			code:   1,
			errOut: []string{"afk daemon is already running (pid 777)"},
		},
		{
			name: "a missing project folder is reported, not created",
			args: []string{"--once"},
			setup: func(r *daemonRig) {
				r.writeConfig("[[project]]\nname = \"gone\"\npath = \"" + filepath.Join(r.cfgHome, "nowhere") + "\"\n")
			},
			code: 0,
			out:  []string{"gone: stopped, because the project folder was not found"},
			check: func(t *testing.T, r *daemonRig) {
				if _, err := os.Stat(filepath.Join(r.cfgHome, "nowhere")); err == nil {
					t.Error("the daemon created the missing project folder")
				}
			},
		},
		{
			name: "worktree path",
			args: []string{"--once"},
			setup: func(r *daemonRig) {
				root := r.project()
				wt := r.t.TempDir()
				r.common[wt] = filepath.Join(root, ".git")
				r.writeConfig("[[project]]\nname = \"demo\"\npath = \"" + wt + "\"\n")
			},
			code:     0,
			launches: 1,
			check: func(t *testing.T, r *daemonRig) {
				root := r.launches[0].Project.Root
				if _, err := os.Stat(ledger.Path(root)); err != nil {
					t.Errorf("no ledger in the launch's project root %s: %v", root, err)
				}
				if _, err := os.Stat(filepath.Join(root, "docs", "PLAN.md")); err != nil {
					t.Errorf("the launch's project root %s is not the main checkout", root)
				}
			},
		},
		{
			name: "stops when asked",
			setup: func(r *daemonRig) {
				root := r.project()
				if err := daemonstate.Pause(root, daemonTestNow); err != nil {
					r.t.Fatal(err)
				}
				r.writeConfig("[[project]]\nname = \"demo\"\npath = \"" + root + "\"\n")
			},
			code:   0,
			errOut: []string{"afk daemon started (pid 4242): 1 project(s), looking every 5s", "Every session is still running"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newDaemonRig(t)
			if tt.setup != nil {
				tt.setup(r)
			}
			var so, se bytes.Buffer
			code := runDaemon(tt.args, &so, &se, r.env())
			t.Logf("exit %d\nstdout: %s\nstderr: %s", code, so.String(), se.String())
			if code != tt.code {
				t.Errorf("exit code = %d, want %d", code, tt.code)
			}
			for _, w := range tt.out {
				if !strings.Contains(so.String(), w) {
					t.Errorf("stdout lacks %q", w)
				}
			}
			for _, w := range tt.errOut {
				if !strings.Contains(se.String(), w) {
					t.Errorf("stderr lacks %q", w)
				}
			}
			if len(r.launches) != tt.launches {
				t.Errorf("launches = %d, want %d", len(r.launches), tt.launches)
			}
			if tt.check != nil {
				tt.check(t, r)
			}
		})
	}
}

// TestRunDaemonStatus runs the report against real files in temp folders.
//
// Load-bearing: the damaged ledger. It must be printed as that project's
// problem, the other projects must still be reported, and the exit code must
// stay 0, because status is how the owner finds out what is wrong.
func TestRunDaemonStatus(t *testing.T) {
	r := newDaemonRig(t)
	paused, damaged, fresh := r.project(), r.project(), r.project()
	wt := t.TempDir()
	r.common[wt] = filepath.Join(fresh, ".git")
	gone := filepath.Join(r.cfgHome, "nowhere")

	if err := daemonstate.Pause(paused, daemonTestNow); err != nil {
		t.Fatal(err)
	}
	if err := daemonstate.WriteState(paused, daemonstate.State{
		At: "2026-10-11T09:00:00Z", Kind: string(daemon.ActWait), Task: "T1.1", Session: "t1-1", Reason: "t1-1 is in phase executing",
	}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Update(paused, func(l *ledger.Ledger) error {
		l.Tasks = append(l.Tasks, ledger.Entry{ID: "T1.1", Status: ledger.Running, Session: "t1-1"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	writeBuildFile(t, damaged, ".afk/build/ledger.json", "{not json")
	r.writeConfig(fmt.Sprintf(`
[[project]]
name = "alpha"
path = %q
[[project]]
name = "beta"
path = %q
[[project]]
name = "gamma"
path = %q
[[project]]
name = "delta"
path = %q
`, paused, damaged, wt, gone))
	if unlock, err := daemonstate.Lock(r.stateDir(), 777, daemonTestNow.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	} else {
		unlock()
	}
	r.alive = true

	var so, se bytes.Buffer
	code := runDaemon([]string{"status"}, &so, &se, r.env())
	t.Logf("exit %d\nstdout:\n%s\nstderr: %s", code, so.String(), se.String())
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	for _, w := range []string{
		"The afk daemon is running (pid 777, started Sun 11 Oct 08:30).",
		"alpha (" + paused + ")",
		`Paused: no new task will start until you run "afk resume alpha"`,
		"Task:    T1.1, in session t1-1",
		"Why:     t1-1 is in phase executing",
		"Since:   Sun 11 Oct 09:00 (30m ago)",
		"Running: session t1-1 has been in this state for 30m",
		"Tasks:   1 running",
		"beta (" + damaged + ")",
		"Problem: ", "ledger file is damaged", "Fix or delete " + ledger.Path(damaged),
		"gamma (" + fresh + ")", // the worktree path, shown as its main checkout
		"The daemon has not looked at this project yet.",
		"Tasks:   none recorded yet",
		"delta (" + gone + ")",
		"The project folder does not exist. Check its path in " + r.configPath(),
	} {
		if !strings.Contains(so.String(), w) {
			t.Errorf("status lacks %q", w)
		}
	}
	if _, err := os.Stat(gone); err == nil {
		t.Error("status created the missing project folder")
	}

	t.Run("a config that cannot be read", func(t *testing.T) {
		r := newDaemonRig(t)
		r.writeConfig("[daemon]\ntick = \"banana\"\n")
		var so, se bytes.Buffer
		if code := runDaemon([]string{"status"}, &so, &se, r.env()); code != 1 || !strings.Contains(se.String(), "tick") {
			t.Errorf("code = %d, stderr = %q; want 1 naming tick", code, se.String())
		}
	})
}

// TestRenderDaemonStatus pins the report's wording per state.
//
// Load-bearing rows: "paused and idle" (the owner must be able to tell that
// nothing is running because of the pause, not because the daemon is broken);
// both "left by a stopped daemon" rows (a state file outlives the daemon, so
// the report must say the daemon is not running, label the state as the last
// one, and not count up a session's running time from a file nobody updates);
// "parked task" (no Running line: a parked session is not at work, and a
// duration there would read as progress).
func TestRenderDaemonStatus(t *testing.T) {
	base := func(ps ...daemonProjectStatus) daemonStatus {
		return daemonStatus{ConfigPath: "/cfg/afk/config.toml", Now: daemonTestNow, Loc: time.UTC, Projects: ps,
			HasMachine: true, PID: 31, StartedAt: "2026-10-09T10:00:00Z", Running: true}
	}
	tests := []struct {
		name string
		in   daemonStatus
		want []string
		not  []string
	}{
		{
			name: "nothing configured, never started",
			in:   daemonStatus{ConfigPath: "/cfg/afk/config.toml", Now: daemonTestNow, Loc: time.UTC},
			want: []string{"has not been started on this machine yet", "No projects are configured", "/cfg/afk/config.toml", "[[project]]"},
		},
		{
			name: "left by a stopped daemon",
			in: func() daemonStatus {
				in := base(daemonProjectStatus{Name: "demo", Root: "/p/demo", HasState: true,
					State: daemonstate.State{At: "2026-10-10T07:15:00Z", Kind: string(daemon.ActIdle), Reason: "all done: every task in the plan is finished"}})
				in.Running = false
				return in
			}(),
			want: []string{"The afk daemon is not running. It was last started Fri 9 Oct 10:00.", "how it left things",
				"Last:    nothing to start", "Why:     all done", "Since:   Sat 10 Oct 07:15 (1d 2h ago)", "Tasks:   none recorded yet"},
			not: []string{"Running:", "Paused:", "Doing:"},
		},
		{
			name: "running task left by a stopped daemon",
			in: func() daemonStatus {
				in := base(daemonProjectStatus{Name: "demo", Root: "/p/demo", HasState: true,
					State: daemonstate.State{At: "2026-10-11T07:10:00Z", Kind: string(daemon.ActWait), Task: "T1.2", Session: "t1-2", Reason: "t1-2 is at work"}})
				in.Running = false
				return in
			}(),
			want: []string{"Last:    waiting", "Task:    T1.2, in session t1-2", "Since:   Sun 11 Oct 07:10 (2h 20m ago)"},
			not:  []string{"Running:", "Doing:"},
		},
		{
			name: "paused and idle",
			in: base(daemonProjectStatus{Name: "demo", Root: "/p/demo", Paused: true, HasState: true,
				State: daemonstate.State{At: "2026-10-11T09:29:40Z", Kind: string(daemon.ActWait), Reason: "paused; no new task will start until `afk resume`"}}),
			want: []string{`Paused: no new task will start until you run "afk resume demo"`, "Doing:   waiting",
				"Why:     paused", "(less than a minute ago)"},
			not: []string{"Running:", "Task:"},
		},
		{
			name: "running task",
			in: base(daemonProjectStatus{Name: "demo", Root: "/p/demo", HasState: true,
				State:  daemonstate.State{At: "2026-10-11T07:10:00Z", Kind: string(daemon.ActLaunch), Task: "T1.2", Session: "t1-2", Reason: "started T1.2 in session t1-2"},
				Counts: map[ledger.Status]int{ledger.Done: 1, ledger.Running: 1}}),
			want: []string{"Doing:   started a task", "Task:    T1.2, in session t1-2", "Since:   Sun 11 Oct 07:10 (2h 20m ago)",
				"Running: session t1-2 has been in this state for 2h 20m", "Tasks:   1 running, 1 done"},
		},
		{
			name: "parked task",
			in: base(daemonProjectStatus{Name: "demo", Root: "/p/demo", HasState: true,
				State:  daemonstate.State{At: "2026-10-11T09:00:00Z", Kind: string(daemon.ActPark), Task: "T1.1", Session: "t1-1", Reason: "waiting on a1b2"},
				Counts: map[ledger.Status]int{ledger.Parked: 1}}),
			want: []string{"Doing:   a task is parked until you answer its inbox items", "Task:    T1.1, in session t1-1", "Tasks:   1 parked"},
			not:  []string{"Running:"},
		},
		{
			name: "machine cannot start agents",
			in: base(daemonProjectStatus{Name: "demo", Root: "/p/demo", HasState: true,
				State: daemonstate.State{At: "2026-10-11T09:00:00Z", Kind: daemon.KindBlocked, Task: "T1.1", Reason: "see inbox item a1b2"}}),
			want: []string{"Doing:   a task is ready, but this machine cannot start agents", "Task:    T1.1\n"},
		},
		{
			name: "missing folder, damaged ledger, unread project, odd state",
			in: base(
				daemonProjectStatus{Name: "gone", Root: "/p/gone", Missing: true},
				daemonProjectStatus{Name: "bad", Root: "/p/bad", LedgerProblem: "ledger file is damaged. Fix or delete /p/bad/.afk/build/ledger.json"},
				daemonProjectStatus{Name: "odd", Root: "/p/odd", HasState: true, State: daemonstate.State{At: "soon", Kind: "from-the-future"}},
			),
			want: []string{"gone (/p/gone)\n  The project folder does not exist. Check its path in /cfg/afk/config.toml.\n",
				"bad (/p/bad)\n  The daemon has not looked at this project yet.\n  Problem: ledger file is damaged. Fix or delete",
				"Doing:   from-the-future", "Since:   unknown"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderDaemonStatus(tt.in)
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("lacks %q in:\n%s", w, got)
				}
			}
			for _, w := range tt.not {
				if strings.Contains(got, w) {
					t.Errorf("holds %q, which it must not, in:\n%s", w, got)
				}
			}
		})
	}
}

// TestDaemonKindTextCoversEveryKind keeps the report in step with the loop:
// a kind the loop writes that this file has no words for would be shown to
// the owner as a bare code word.
func TestDaemonKindTextCoversEveryKind(t *testing.T) {
	kinds := []string{
		string(daemon.ActStop), string(daemon.ActWait), string(daemon.ActCreate), string(daemon.ActShip),
		string(daemon.ActFail), string(daemon.ActPark), string(daemon.ActHostGone), string(daemon.ActStall),
		string(daemon.ActUnpark), string(daemon.ActLaunch), string(daemon.ActIdle),
		daemon.KindStopped, daemon.KindBlocked,
	}
	for _, k := range kinds {
		if got := daemonKindText(k); got == k || got == "" {
			t.Errorf("daemonKindText(%q) = %q, want plain words", k, got)
		}
	}
}

// TestDaemonLaunchConfig pins the session the daemon asks for. All three
// switches are load-bearing: without Autonomous the conductor asks the owner
// questions, without Unattended the agent CLI asks permission, and without
// NoGitSpice (or with a Base) the launch depends on how the main checkout
// happens to be set up. That the unattended mode then reaches the command
// line is pinned by internal/session's argv tests.
func TestDaemonLaunchConfig(t *testing.T) {
	tests := []struct {
		name     string
		req      daemon.LaunchRequest
		provider string
	}{
		{name: "default provider", provider: "",
			req: daemon.LaunchRequest{Project: daemon.Project{Name: "demo", Root: "/p/demo"}, Task: "T1.1", Session: "t1-1", Description: "Implement T1.1 from docs/PLAN.md"}},
		{name: "configured provider is passed through", provider: "some-agent",
			req: daemon.LaunchRequest{Project: daemon.Project{Name: "other", Root: "/p/other"}, Task: "T4.2a", Session: "t4-2a-2", Description: "Implement T4.2a from PLAN.md"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := daemonLaunchConfig(tt.req, tt.provider)
			if !got.Autonomous || !got.Unattended || !got.NoGitSpice {
				t.Errorf("Autonomous=%v Unattended=%v NoGitSpice=%v, want all true", got.Autonomous, got.Unattended, got.NoGitSpice)
			}
			if got.Base != "" {
				t.Errorf("Base = %q, want empty so the plain-git trunk is used", got.Base)
			}
			if got.Scratch || got.DryRun {
				t.Errorf("Scratch=%v DryRun=%v, want both false", got.Scratch, got.DryRun)
			}
			if got.Name != tt.req.Session || got.Description != tt.req.Description || got.RepoDir != tt.req.Project.Root || got.Provider != tt.provider {
				t.Errorf("got %+v, want name, description, repo and provider passed through from %+v", got, tt.req)
			}
		})
	}
}

// preflightProvider is a provider whose two checks are scripted. It embeds
// the interface and defines only what the daemon's preflight calls, so a
// method added to the interface later does not break it.
type preflightProvider struct {
	provider.Provider
	modeOK, pluginOK       bool
	modeSteps, pluginSteps string
	gotDir                 string
	gotDeps                provider.UnattendedDeps
}

func (p *preflightProvider) UnattendedPreflight(d provider.UnattendedDeps) (bool, string) {
	p.gotDeps = d
	return p.modeOK, p.modeSteps
}

func (p *preflightProvider) Preflight(dir string, _ func(string, string, ...string) (string, error)) (bool, string) {
	p.gotDir = dir
	return p.pluginOK, p.pluginSteps
}

// TestDaemonPreflight pins how the two checks combine.
//
// Load-bearing rows: "plugin missing" (session creation only warns about a
// missing plugin, but the daemon must refuse, because nobody is there to see
// the warning); "both fail" (one inbox item must list both fixes).
func TestDaemonPreflight(t *testing.T) {
	tests := []struct {
		name             string
		modeOK, pluginOK bool
		wantOK           bool
		want, not        []string
	}{
		{name: "both pass", modeOK: true, pluginOK: true, wantOK: true, not: []string{"accept", "install"}},
		{name: "mode refused", pluginOK: true, want: []string{"accept the warning"}, not: []string{"install"}},
		{name: "plugin missing", modeOK: true, want: []string{"install the plugin"}, not: []string{"accept"}},
		{name: "both fail", want: []string{"accept the warning\n\ninstall the plugin"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &preflightProvider{modeOK: tt.modeOK, pluginOK: tt.pluginOK, modeSteps: "accept the warning\n", pluginSteps: "install the plugin"}
			ok, steps := daemonPreflight(p, provider.UnattendedDeps{Home: "/home/x"}, nil)(daemon.Project{Name: "demo", Root: "/p/demo"})
			if ok != tt.wantOK {
				t.Errorf("ok = %v, want %v", ok, tt.wantOK)
			}
			for _, w := range tt.want {
				if !strings.Contains(steps, w) {
					t.Errorf("steps %q lack %q", steps, w)
				}
			}
			for _, w := range tt.not {
				if strings.Contains(steps, w) {
					t.Errorf("steps %q hold %q, which they must not", steps, w)
				}
			}
			if p.gotDir != "/p/demo" || p.gotDeps.Home != "/home/x" {
				t.Errorf("checks ran with dir %q and home %q, want the project root and the given home", p.gotDir, p.gotDeps.Home)
			}
		})
	}
}

// TestDaemonNotifier pins who gets a push notification from the daemon, with
// a fake sender: nothing here reaches a network.
//
// Load-bearing rows: "send fails" and "config unreadable" (a notification
// must never fail a pass; the only trace is a warning in the log, and the
// warning must not repeat the address, which works as a password); "no
// address" (most projects have none, and that is silence, not a warning).
func TestDaemonNotifier(t *testing.T) {
	const url = "https://user:secret@ntfy.example/topic"
	item := inbox.Item{ID: "a1b2", Task: "T1.1", Kind: "failure", Blocking: true, Title: "It broke", Body: "Details."}
	quiet := inbox.Item{ID: "c3d4", Kind: "notice", Title: "FYI"}
	tests := []struct {
		name    string
		url     string
		cfgErr  error
		sendErr error
		call    func(n daemonNotifier)
		want    *notify.NtfyMessage
		logHas  string
	}{
		{name: "item", url: url, call: func(n daemonNotifier) { n.item(daemon.Project{Name: "demo", Root: "/p"}, item) },
			want: &notify.NtfyMessage{Title: "afk: It broke", Body: "Details.\n\nafk inbox show a1b2 (T1.1)", Priority: 5}},
		{name: "item that does not notify", url: url, call: func(n daemonNotifier) { n.item(daemon.Project{Name: "demo", Root: "/p"}, quiet) }},
		{name: "no address", call: func(n daemonNotifier) { n.item(daemon.Project{Name: "demo", Root: "/p"}, item) }},
		{name: "idle", url: url, call: func(n daemonNotifier) {
			n.idle(daemon.Project{Name: "demo", Root: "/p"}, "demo is waiting on you", "waiting on owner: T1.1 parked")
		},
			want: &notify.NtfyMessage{Title: "afk: demo is waiting on you", Body: "waiting on owner: T1.1 parked", Priority: 3}},
		{name: "idle, no address", call: func(n daemonNotifier) { n.idle(daemon.Project{Name: "demo", Root: "/p"}, "t", "b") }},
		{name: "send fails", url: url, sendErr: errors.New("could not reach https://user:xxxxx@ntfy.example/topic"),
			call:   func(n daemonNotifier) { n.item(daemon.Project{Name: "demo", Root: "/p"}, item) },
			want:   &notify.NtfyMessage{Title: "afk: It broke", Body: "Details.\n\nafk inbox show a1b2 (T1.1)", Priority: 5},
			logHas: "demo: warning: the notification could not be sent"},
		{name: "config unreadable", cfgErr: errors.New("afk.local.toml: not valid TOML"),
			call:   func(n daemonNotifier) { n.idle(daemon.Project{Name: "demo", Root: "/p"}, "t", "b") },
			logHas: "demo: warning: the project config could not be read"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sent []notify.NtfyMessage
			var log bytes.Buffer
			n := daemonNotifier{
				loadConfig: func(root string) (projectconfig.Config, error) {
					if root != "/p" {
						t.Errorf("config loaded from %q, want the project root", root)
					}
					return projectconfig.Config{Notify: projectconfig.Notify{NtfyURL: tt.url}}, tt.cfgErr
				},
				send: func(_ context.Context, to string, m notify.NtfyMessage) error {
					if to != tt.url {
						t.Errorf("sent to %q, want the configured address", to)
					}
					sent = append(sent, m)
					return tt.sendErr
				},
				logf: daemonLogger(&log, func() time.Time { return daemonTestNow }),
			}
			tt.call(n)
			switch {
			case tt.want == nil && len(sent) != 0:
				t.Errorf("sent %+v, want nothing", sent)
			case tt.want != nil && (len(sent) != 1 || sent[0] != *tt.want):
				t.Errorf("sent %+v, want exactly %+v", sent, *tt.want)
			}
			if tt.logHas == "" && log.Len() != 0 {
				t.Errorf("logged %q, want silence", log.String())
			}
			if !strings.Contains(log.String(), tt.logHas) {
				t.Errorf("log %q lacks %q", log.String(), tt.logHas)
			}
			if strings.Contains(log.String(), "secret") {
				t.Errorf("log %q repeats the address's password", log.String())
			}
			if tt.logHas != "" && !strings.HasPrefix(log.String(), "2026-10-11T09:30:00Z ") {
				t.Errorf("log line %q does not start with its time", log.String())
			}
		})
	}
}

// TestDaemonProjects pins how configured paths become the loop's projects.
//
// Load-bearing: the worktree row (the ledger and inbox live in the main
// checkout only) and the duplicate row (two loops over one ledger would run
// two tasks at once in a project that is promised one).
func TestDaemonProjects(t *testing.T) {
	common := map[string]string{
		"/p/app":    "/p/app/.git",
		"/wt/app-x": "/p/app/.git",
		"/p/plain":  "",
	}
	git := func(dir string) string { return common[dir] }
	tests := []struct {
		name        string
		in          []daemonconfig.Project
		want        []daemon.Project
		wantDropped []string
	}{
		{name: "main checkout and a folder outside git",
			in:   []daemonconfig.Project{{Name: "app", Path: "/p/app"}, {Name: "plain", Path: "/p/plain"}},
			want: []daemon.Project{{Name: "app", Root: "/p/app"}, {Name: "plain", Root: "/p/plain"}}},
		{name: "a worktree path becomes its main checkout",
			in:   []daemonconfig.Project{{Name: "x", Path: "/wt/app-x"}},
			want: []daemon.Project{{Name: "x", Root: "/p/app"}}},
		{name: "the same checkout twice keeps the first",
			in:          []daemonconfig.Project{{Name: "app", Path: "/p/app"}, {Name: "x", Path: "/wt/app-x"}},
			want:        []daemon.Project{{Name: "app", Root: "/p/app"}},
			wantDropped: []string{"x is left out", "same checkout as app"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, dropped := daemonProjects(tt.in, git)
			if fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Errorf("projects = %v, want %v", got, tt.want)
			}
			all := strings.Join(dropped, "\n")
			if len(tt.wantDropped) == 0 && len(dropped) != 0 {
				t.Errorf("dropped %q, want nothing", dropped)
			}
			for _, w := range tt.wantDropped {
				if !strings.Contains(all, w) {
					t.Errorf("dropped %q lacks %q", all, w)
				}
			}
		})
	}
}
