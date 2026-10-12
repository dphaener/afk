package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	"github.com/dphaener/afk/internal/session"
	"github.com/dphaener/afk/internal/ship"
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
// .git entry) holding a two-task master plan and the empty ledger `afk ledger
// init` writes for a new project. The ledger has to be there: the loop starts
// nothing in a project that has no ledger file at all.
func (r *daemonRig) project() string {
	r.t.Helper()
	root := r.t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		r.t.Fatal(err)
	}
	writeBuildFile(r.t, root, "docs/PLAN.md", daemonTestPlan)
	if err := ledger.Init(root, nil); err != nil {
		r.t.Fatal(err)
	}
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

	// The stop marker is read from the project's own files, not from the
	// daemon's state file, so the report is right with no daemon running.
	t.Run("a stopped line", func(t *testing.T) {
		r := newDaemonRig(t)
		stopped, damaged := r.project(), r.project()
		if err := daemonstate.Stop(stopped, "T1.1", "the deploy did not come up healthy within 15m0s", daemonTestNow); err != nil {
			t.Fatal(err)
		}
		writeBuildFile(t, damaged, ".afk/build/stopped", "{not json")
		r.writeConfig(fmt.Sprintf("[[project]]\nname = \"alpha\"\npath = %q\n[[project]]\nname = \"beta\"\npath = %q\n", stopped, damaged))
		var so, se bytes.Buffer
		if code := runDaemon([]string{"status"}, &so, &se, r.env()); code != 0 {
			t.Fatalf("exit code = %d, stderr %q", code, se.String())
		}
		for _, w := range []string{
			`Stopped: the line is stopped after T1.1: the deploy did not come up healthy within 15m0s. No new task will start until you run "afk resume alpha", which clears the stop.`,
			`Stopped: the line is stopped. No new task will start until you run "afk resume beta", which clears the stop.`,
		} {
			if !strings.Contains(so.String(), w) {
				t.Errorf("status lacks %q in:\n%s", w, so.String())
			}
		}
	})

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
			name: "stopped line, daemon idle behind it",
			in: base(daemonProjectStatus{Name: "demo", Root: "/p/demo", HasState: true,
				Stopped: true, StopTask: "T1.1", StopReason: "the deploy did not come up healthy within 15m0s",
				State:  daemonstate.State{At: "2026-10-11T09:00:00Z", Kind: daemon.KindLineStopped, Reason: "the line is stopped: the deploy did not come up healthy within 15m0s"},
				Counts: map[ledger.Status]int{ledger.Done: 1}}),
			want: []string{`Stopped: the line is stopped after T1.1: the deploy did not come up healthy within 15m0s. No new task will start until you run "afk resume demo", which clears the stop.`,
				"Doing:   the line is stopped, so no new task will start", "Since:   Sun 11 Oct 09:00 (30m ago)"},
			not: []string{"Running:", "line_stopped"},
		},
		{
			name: "stopped line, its task still shipping",
			in: base(daemonProjectStatus{Name: "demo", Root: "/p/demo", HasState: true,
				Stopped: true, StopTask: "T1.1", StopReason: "no answer from the deploy",
				State: daemonstate.State{At: "2026-10-11T09:00:00Z", Kind: daemon.KindLineStopped, Task: "T1.1", Session: "t1-1",
					Reason: "the line is stopped; T1.1 is marked done once it is cleared"}}),
			want: []string{`afk resume demo`, "Stopped: the line is stopped after T1.1: no answer from the deploy.", "Task:    T1.1, in session t1-1"},
			not:  []string{"Running:"},
		},
		{
			name: "stopped by a marker nobody can read, daemon not looked yet",
			in:   base(daemonProjectStatus{Name: "demo", Root: "/p/demo", Stopped: true}),
			want: []string{`Stopped: the line is stopped. No new task will start until you run "afk resume demo"`, "The daemon has not looked at this project yet."},
		},
		{
			name: "shipping",
			in: base(daemonProjectStatus{Name: "demo", Root: "/p/demo", HasState: true,
				State:  daemonstate.State{At: "2026-10-11T09:29:50Z", Kind: string(daemon.ActShipStep), Task: "T1.1", Session: "t1-1", Reason: "shipping T1.1: waiting for the checks on pull request 7"},
				Counts: map[ledger.Status]int{ledger.Shipping: 1}}),
			want: []string{"Doing:   shipping a finished task", "Task:    T1.1, in session t1-1", "Why:     shipping T1.1: waiting for the checks",
				"Updated: Sun 11 Oct 09:29 (less than a minute ago)", "Tasks:   1 shipping"},
			not: []string{"Running:", "Since:", "Stopped:"},
		},
		{
			name: "owner task waiting",
			in: base(daemonProjectStatus{Name: "demo", Root: "/p/demo", HasState: true,
				State:  daemonstate.State{At: "2026-10-11T09:00:00Z", Kind: string(daemon.ActOwner), Task: "T0.0", Reason: "T0.0 is the owner's to do; see inbox item a1b2"},
				Counts: map[ledger.Status]int{ledger.Parked: 1}}),
			want: []string{"Doing:   the next task is yours to do, and is waiting in your inbox", "Task:    T0.0\n", "Since:   Sun 11 Oct 09:00"},
			not:  []string{"Running:", "owner_task"},
		},
		{
			name: "no ledger",
			in: base(daemonProjectStatus{Name: "demo", Root: "/p/demo", HasState: true,
				State: daemonstate.State{At: "2026-10-11T09:00:00Z", Kind: string(daemon.ActNoLedger), Reason: "no task ledger; see inbox item a1b2"}}),
			want: []string{"Doing:   nothing is started, because this project has no task ledger yet", "Why:     no task ledger; see inbox item a1b2", "Tasks:   none recorded yet"},
			not:  []string{"Running:", "no_ledger:"},
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
// the owner as a bare code word. It ranges over the loop's own list, so a
// kind added there fails here until it has words.
//
// The second half is as load-bearing as the first: two kinds with the same
// words could not be told apart by the owner, which is how "shipping" would
// hide behind "waiting".
func TestDaemonKindTextCoversEveryKind(t *testing.T) {
	seen := map[string]string{}
	for _, k := range daemon.Kinds() {
		got := daemonKindText(k)
		if got == k || got == "" {
			t.Errorf("daemonKindText(%q) = %q, want plain words", k, got)
		}
		if strings.Contains(got, "_") {
			t.Errorf("daemonKindText(%q) = %q holds a code word", k, got)
		}
		if other, dup := seen[got]; dup {
			t.Errorf("daemonKindText gives %q and %q the same words (%q)", k, other, got)
		}
		seen[got] = k
	}
	if got := daemonKindText("from-the-future"); got != "from-the-future" {
		t.Errorf("an unknown kind is shown as %q, want it as it is", got)
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
		{name: "urgent, for an item that is not blocking", url: url, call: func(n daemonNotifier) {
			n.urgent(daemon.Project{Name: "demo", Root: "/p"}, inbox.Item{ID: "e5f6", Task: "T1.1", Kind: "failure", Title: "The line is stopped after T1.1", Body: "Check the deploy."})
		},
			want: &notify.NtfyMessage{Title: "afk: The line is stopped after T1.1", Body: "Check the deploy.\n\nafk inbox show e5f6 (T1.1)", Priority: 5}},
		{name: "urgent, for a kind that would not notify", url: url, call: func(n daemonNotifier) { n.urgent(daemon.Project{Name: "demo", Root: "/p"}, quiet) },
			want: &notify.NtfyMessage{Title: "afk: FYI", Body: "afk inbox show c3d4", Priority: 5}},
		{name: "urgent, no address", call: func(n daemonNotifier) { n.urgent(daemon.Project{Name: "demo", Root: "/p"}, item) }},
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

// TestRealDaemonDeps pins that the real wiring sets the calls the loop treats
// as optional. Each nil is silent, which is why this is a test and not a
// review comment: with no Ship a finished task waits for ever in `shipping`,
// with no SendPrompt an answered task is never resumed, with no Clean a
// shipped task leaves its worktree and branch behind, and with no
// NotifyUrgent a failed deploy arrives at an ordinary priority.
//
// Building the deps runs nothing: no git, no agent CLI, no network.
func TestRealDaemonDeps(t *testing.T) {
	var log bytes.Buffer
	d := realDaemonDeps(&preflightProvider{}, "", t.TempDir(), func() time.Time { return daemonTestNow }, &log,
		daemonLogger(&log, func() time.Time { return daemonTestNow }))
	for name, isNil := range map[string]bool{
		"Now": d.Now == nil, "LoadBuild": d.LoadBuild == nil, "Sessions": d.Sessions == nil,
		"Preflight": d.Preflight == nil, "Launch": d.Launch == nil, "RecordTokens": d.RecordTokens == nil,
		"Notify": d.Notify == nil, "NotifyIdle": d.NotifyIdle == nil, "Log": d.Log == nil,
		"Ship": d.Ship == nil, "SendPrompt": d.SendPrompt == nil, "Clean": d.Clean == nil,
		"NotifyUrgent": d.NotifyUrgent == nil,
	} {
		if isNil {
			t.Errorf("realDaemonDeps leaves %s nil", name)
		}
	}
	if daemonSendWait != 2*time.Second {
		t.Errorf("daemonSendWait = %s, want 2s: a pass must not sit on a dead socket", daemonSendWait)
	}
}

// shipCall is one command the fake runner was asked for.
type shipCall struct{ dir, cmd string }

// shipRig is fakes for everything shipDeps reaches the machine through.
type shipRig struct {
	calls   []shipCall
	typed   []string
	items   []inbox.Item
	itemErr error
	healthy []string
}

func (r *shipRig) wiring() shipWiring {
	return shipWiring{
		run: func(dir, name string, args ...string) (string, error) {
			r.calls = append(r.calls, shipCall{dir, strings.Join(append([]string{name}, args...), " ")})
			return "", nil
		},
		health: func(url string) (bool, error) {
			r.healthy = append(r.healthy, url)
			return true, nil
		},
		sendInput: func(worktree, session, text string) error {
			r.typed = append(r.typed, worktree+"|"+session+"|"+text)
			return nil
		},
		listInbox: func(string) ([]inbox.Item, error) { return r.items, r.itemErr },
		now:       func() time.Time { return daemonTestNow },
	}
}

// TestDaemonShipDeps checks that every field ship.Advance calls is set and goes to
// the right place.
//
// Load-bearing:
//   - "no field is nil", by reflection, so a field added to ship.Deps later
//     fails here until it is wired (Advance calls each without asking).
//   - the two "no session on disk" rows: git and gh must then run in the main
//     checkout, since the worktree is gone, and nothing may be typed.
//   - the marker rows read and write the project's own files, which is what
//     lets a killed daemon come back to the same poll time and the same stop.
func TestDaemonShipDeps(t *testing.T) {
	root, wt := t.TempDir(), t.TempDir()
	var saved []ship.Progress
	var stops []string
	request := func(worktree string) daemon.ShipRequest {
		return daemon.ShipRequest{
			Project:  daemon.Project{Name: "demo", Root: root},
			Entry:    ledger.Entry{ID: "T1.1", Status: ledger.Shipping, Session: "t1-1", Branch: "t1-1"},
			Worktree: worktree,
			Save:     func(p ship.Progress) error { saved = append(saved, p); return nil },
			Stop:     func(task, reason string) error { stops = append(stops, task+": "+reason); return nil },
		}
	}

	t.Run("no field is nil", func(t *testing.T) {
		for _, worktree := range []string{wt, ""} {
			r := &shipRig{}
			v := reflect.ValueOf(shipDeps(request(worktree), r.wiring()))
			for i := 0; i < v.NumField(); i++ {
				if v.Field(i).IsNil() {
					t.Errorf("worktree %q: ship.Deps.%s is nil", worktree, v.Type().Field(i).Name)
				}
			}
		}
	})

	t.Run("git and gh with a session on disk", func(t *testing.T) {
		r := &shipRig{}
		d := shipDeps(request(wt), r.wiring())
		_, _ = d.Git.LocalSHA("t1-1")
		_, _ = d.Git.Clean()
		_ = d.GH.Ready()
		if len(r.calls) != 3 {
			t.Fatalf("calls = %+v, want three", r.calls)
		}
		if r.calls[0].dir != root || !strings.HasPrefix(r.calls[0].cmd, "git ") {
			t.Errorf("a branch read ran as %+v, want git in the main checkout", r.calls[0])
		}
		if r.calls[1].dir != wt || !strings.HasPrefix(r.calls[1].cmd, "git ") {
			t.Errorf("the clean read ran as %+v, want git in the worktree", r.calls[1])
		}
		if r.calls[2].dir != wt || !strings.HasPrefix(r.calls[2].cmd, "gh ") {
			t.Errorf("gh ran as %+v, want gh in the worktree", r.calls[2])
		}
	})

	t.Run("git and gh with no session on disk", func(t *testing.T) {
		r := &shipRig{}
		d := shipDeps(request(""), r.wiring())
		_, _ = d.Git.LocalSHA("t1-1")
		_ = d.GH.Ready()
		if len(r.calls) != 2 || r.calls[0].dir != root || r.calls[1].dir != root {
			t.Errorf("calls = %+v, want git and gh both in the main checkout", r.calls)
		}
		if _, err := d.Git.Clean(); err == nil {
			t.Error("Git.Clean with no worktree gave no error; it has nothing to read")
		}
	})

	t.Run("prompt", func(t *testing.T) {
		r := &shipRig{}
		if err := shipDeps(request(wt), r.wiring()).Prompt("read ci-failure.md"); err != nil {
			t.Fatal(err)
		}
		if want := []string{wt + "|t1-1|read ci-failure.md"}; !reflect.DeepEqual(r.typed, want) {
			t.Errorf("typed %q, want %q", r.typed, want)
		}
	})

	t.Run("prompt with no session on disk", func(t *testing.T) {
		r := &shipRig{}
		err := shipDeps(request(""), r.wiring()).Prompt("read ci-failure.md")
		if err == nil || !strings.Contains(err.Error(), "t1-1") || len(r.typed) != 0 {
			t.Errorf("err = %v, typed = %q; want an error naming the session and nothing typed", err, r.typed)
		}
	})

	t.Run("markers, clock, save and stop", func(t *testing.T) {
		r := &shipRig{}
		d := shipDeps(request(wt), r.wiring())
		if !d.Now().Equal(daemonTestNow) {
			t.Errorf("Now = %s", d.Now())
		}
		if d.StopMarker() {
			t.Error("StopMarker is true in a project with no marker")
		}
		if err := daemonstate.Stop(root, "T1.1", "deploy down", daemonTestNow); err != nil {
			t.Fatal(err)
		}
		if !d.StopMarker() {
			t.Error("StopMarker is false after the marker was written")
		}
		if !d.LastPoll().IsZero() || !d.ReadFailingSince().IsZero() {
			t.Errorf("LastPoll = %s, ReadFailingSince = %s, want both zero before any poll", d.LastPoll(), d.ReadFailingSince())
		}
		if err := d.MarkPoll(daemonTestNow); err != nil {
			t.Fatal(err)
		}
		if !d.LastPoll().Equal(daemonTestNow) || !daemonstate.LastShipPoll(root).Equal(daemonTestNow) {
			t.Errorf("LastPoll = %s, want the marked time, kept in the project's files", d.LastPoll())
		}
		if err := d.MarkReadFailure(daemonTestNow.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		if !d.ReadFailingSince().Equal(daemonTestNow.Add(time.Minute)) {
			t.Errorf("ReadFailingSince = %s, want the marked time", d.ReadFailingSince())
		}
		if err := d.ClearReadFailure(); err != nil {
			t.Fatal(err)
		}
		if !d.ReadFailingSince().IsZero() {
			t.Errorf("ReadFailingSince = %s after a clear, want zero", d.ReadFailingSince())
		}
		if err := d.Save(ship.Progress{State: ship.Pushed}); err != nil || len(saved) != 1 || saved[0].State != ship.Pushed {
			t.Errorf("Save did not reach the request's Save: err %v, saved %+v", err, saved)
		}
		if err := d.Stop("T1.1", "no answer"); err != nil || !reflect.DeepEqual(stops, []string{"T1.1: no answer"}) {
			t.Errorf("Stop did not reach the request's Stop: err %v, stops %q", err, stops)
		}
		if ok, err := d.Health("https://example.test/up"); !ok || err != nil || len(r.healthy) != 1 {
			t.Errorf("Health did not reach the wiring: ok %v err %v asked %q", ok, err, r.healthy)
		}
	})

	t.Run("stop with none on the request writes the marker", func(t *testing.T) {
		root := t.TempDir()
		req := request(wt)
		req.Project.Root, req.Stop = root, nil
		if err := shipDeps(req, (&shipRig{}).wiring()).Stop("T1.1", "no answer"); err != nil {
			t.Fatal(err)
		}
		if task, reason, ok := daemonstate.ReadStop(root); !ok || task != "T1.1" || reason != "no answer" {
			t.Errorf("marker = %q, %q, %v", task, reason, ok)
		}
	})

	t.Run("hand-back file", func(t *testing.T) {
		d := shipDeps(request(wt), (&shipRig{}).wiring())
		path := filepath.Join(wt, "ci-failure.md")
		if _, err := d.ReadFile(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("ReadFile of a missing file = %v, want one that is fs.ErrNotExist", err)
		}
		if err := d.WriteFile(path, []byte("Head commit: abc\n")); err != nil {
			t.Fatal(err)
		}
		if got, err := d.ReadFile(path); err != nil || string(got) != "Head commit: abc\n" {
			t.Errorf("read back %q, %v", got, err)
		}
	})

	t.Run("env var answers", func(t *testing.T) {
		r := &shipRig{items: []inbox.Item{{Task: "T1.1", Kind: "env_var", Title: "Set API_KEY before T1.1 merges", Answer: "set"}}}
		d := shipDeps(request(wt), r.wiring())
		if !d.EnvAnswered("API_KEY") || d.EnvAnswered("OTHER") {
			t.Error("EnvAnswered does not follow the inbox")
		}
		r.itemErr = errors.New("inbox unreadable")
		if d.EnvAnswered("API_KEY") {
			t.Error("EnvAnswered is true when the inbox cannot be listed; the merge must wait")
		}
	})
}

// TestDaemonEnvAnswered is the "has the owner said this variable is set" read.
//
// Load-bearing: "longer name" (an answer about MY_API_KEY must not release a
// merge waiting on API_KEY, which a substring test would do), "open" (asked
// is not answered) and "another task" (the same variable for a different
// task is a different question).
func TestDaemonEnvAnswered(t *testing.T) {
	item := func(task, kind, title, answer string) inbox.Item {
		return inbox.Item{Task: task, Kind: kind, Title: title, Answer: answer}
	}
	tests := []struct {
		name     string
		items    []inbox.Item
		variable string
		want     bool
	}{
		{"answered", []inbox.Item{item("T1.1", "env_var", "Set API_KEY before T1.1 merges", "done")}, "API_KEY", true},
		{"open", []inbox.Item{item("T1.1", "env_var", "Set API_KEY before T1.1 merges", "")}, "API_KEY", false},
		{"another task", []inbox.Item{item("T1.2", "env_var", "Set API_KEY before T1.2 merges", "done")}, "API_KEY", false},
		{"another kind", []inbox.Item{item("T1.1", "failure", "Set API_KEY before T1.1 merges", "done")}, "API_KEY", false},
		{"another variable", []inbox.Item{item("T1.1", "env_var", "Set DB_URL before T1.1 merges", "done")}, "API_KEY", false},
		{"longer name", []inbox.Item{item("T1.1", "env_var", "Set MY_API_KEY before T1.1 merges", "done")}, "API_KEY", false},
		{"second item answers", []inbox.Item{
			item("T1.1", "env_var", "Set DB_URL before T1.1 merges", "done"),
			item("T1.1", "env_var", "Set API_KEY before T1.1 merges", "done")}, "API_KEY", true},
		{"no items", nil, "API_KEY", false},
		{"empty variable", []inbox.Item{item("T1.1", "env_var", "Set  before T1.1 merges", "done")}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := envAnswered(tt.items, "T1.1", tt.variable); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// TestDaemonShipRequest pins what the loop's request becomes for ship.
//
// Load-bearing: "config base wins" and "session base fills an unset key" are
// the two halves of where the base comes from; "no session, no base" must
// pass both on empty, because ship parks on a missing base and a guess here
// would open a pull request against the wrong branch.
func TestDaemonShipRequest(t *testing.T) {
	entry := ledger.Entry{ID: "T1.1", Status: ledger.Shipping, Session: "t1-1", Branch: "t1-1"}
	progress := ship.Progress{State: ship.PROpen, PR: 7}
	tests := []struct {
		name           string
		worktree, base string
		cfgBase        string
		wantCfgBase    string
		wantDir        string
	}{
		{name: "session base fills an unset key", worktree: "/wt/t1-1", base: "main", wantCfgBase: "main", wantDir: "/wt/t1-1/.afk/t1-1"},
		{name: "config base wins", worktree: "/wt/t1-1", base: "main", cfgBase: "release", wantCfgBase: "release", wantDir: "/wt/t1-1/.afk/t1-1"},
		{name: "no session, config base", cfgBase: "release", wantCfgBase: "release"},
		{name: "no session, no base"},
		{name: "stale status, no base", worktree: "/wt/t1-1", wantDir: "/wt/t1-1/.afk/t1-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := daemon.ShipRequest{Project: daemon.Project{Root: "/p"}, Entry: entry, Worktree: tt.worktree, Base: tt.base, Progress: progress}
			got := shipRequest(req, projectconfig.Ship{Base: tt.cfgBase, MergeMethod: "squash", CIReruns: 3})
			if got.Task != "T1.1" || got.Session != "t1-1" || got.Branch != "t1-1" || got.Progress != progress {
				t.Errorf("task, session, branch or progress not passed through: %+v", got)
			}
			if got.Base != tt.base || got.Config.Base != tt.wantCfgBase {
				t.Errorf("Base = %q, Config.Base = %q; want %q and %q", got.Base, got.Config.Base, tt.base, tt.wantCfgBase)
			}
			if got.SessionDir != filepath.FromSlash(tt.wantDir) {
				t.Errorf("SessionDir = %q, want %q", got.SessionDir, tt.wantDir)
			}
			if got.Config.MergeMethod != "squash" || got.Config.CIReruns != 3 {
				t.Errorf("the rest of [ship] was not passed through: %+v", got.Config)
			}
		})
	}
}

// TestDaemonShip runs the loop's Ship over fakes.
//
// Load-bearing:
//   - "config unreadable" must be a wait that carries the error and runs
//     nothing: a broken afk.toml must not crash the pass, must not ship on
//     settings nobody can read, and clears itself once the file is fixed.
//   - "no session and no base" must reach ship unchanged and come back as
//     ship's own park, with no command run.
//   - "last state" shows ship.Advance is really what is called.
func TestDaemonShip(t *testing.T) {
	entry := ledger.Entry{ID: "T1.1", Status: ledger.Shipping, Session: "t1-1", Branch: "t1-1"}
	tests := []struct {
		name     string
		cfgErr   error
		progress ship.Progress
		wantKind ship.OutcomeKind
		wantHas  string
	}{
		{name: "config unreadable", cfgErr: errors.New("afk.toml is not valid TOML"), progress: ship.Progress{State: ship.PROpen, PR: 7},
			wantKind: ship.OutcomeWait, wantHas: "afk.toml is not valid TOML"},
		{name: "no session and no base", progress: ship.Progress{}, wantKind: ship.OutcomePark, wantHas: "base"},
		{name: "last state", progress: ship.Progress{State: ship.DeployHealthy, PR: 7, MergedSHA: "abc"}, wantKind: ship.OutcomeDone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &shipRig{}
			root := t.TempDir()
			fn := daemonShip(func(got string) (projectconfig.Config, error) {
				if got != root {
					t.Errorf("config loaded from %q, want the project root", got)
				}
				return projectconfig.Config{Ship: projectconfig.Ship{MergeMethod: "merge", Deploys: true, DeployTimeout: 15 * time.Minute}}, tt.cfgErr
			}, r.wiring())
			out := fn(daemon.ShipRequest{
				Project: daemon.Project{Name: "demo", Root: root}, Entry: entry, Progress: tt.progress,
				Save: func(ship.Progress) error { return nil },
				Stop: func(string, string) error { return nil },
			})
			if out.Kind != tt.wantKind {
				t.Fatalf("outcome = %+v, want kind %q", out, tt.wantKind)
			}
			if !strings.Contains(out.Reason, tt.wantHas) {
				t.Errorf("reason %q lacks %q", out.Reason, tt.wantHas)
			}
			if out.Progress != tt.progress {
				t.Errorf("progress = %+v, want it unchanged (%+v)", out.Progress, tt.progress)
			}
			if len(r.calls) != 0 || len(r.typed) != 0 {
				t.Errorf("ran %+v and typed %q, want nothing", r.calls, r.typed)
			}
		})
	}
}

// TestDaemonWriteFileAtomic: a new file, a replaced file, and a folder that is not
// there. The last row is the load-bearing one: the hand-back file must not
// bring a cleaned-up session's folder back, and a failed write must not leave
// its temp file behind.
func TestDaemonWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ci-failure.md")
	for _, text := range []string{"first\n", "second, and longer\n"} {
		if err := writeFileAtomic(path, []byte(text)); err != nil {
			t.Fatal(err)
		}
		if got, err := os.ReadFile(path); err != nil || string(got) != text {
			t.Errorf("read back %q, %v; want %q", got, err, text)
		}
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v (%v), want 0644 like the session's other files", fi.Mode().Perm(), err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("folder holds %d entries, want only the file (no temp file left)", len(entries))
	}
	gone := filepath.Join(dir, "gone")
	if err := writeFileAtomic(filepath.Join(gone, "ci-failure.md"), []byte("x")); err == nil {
		t.Error("writing into a folder that is not there gave no error")
	}
	if _, err := os.Stat(gone); err == nil {
		t.Error("the missing folder was created")
	}
}

// TestDaemonCleanOptions pins how a shipped session is cleaned.
//
// Both rows are load-bearing. With a merge commit in the ledger the branch
// must be deleted without the local "is it merged" check having the last
// word, or every squash- and rebase-merged branch is kept for ever. Without
// one, that check must still decide, so work GitHub never confirmed merged
// is not deleted on the daemon's say-so.
func TestDaemonCleanOptions(t *testing.T) {
	tests := []struct {
		name  string
		entry ledger.Entry
		want  session.CleanOptions
	}{
		{"merged_sha recorded", ledger.Entry{ID: "T1.1", Session: "t1-1", MergedSHA: "abc123"},
			session.CleanOptions{DeleteBranch: true, ConfirmedUnmerged: true}},
		{"no merged_sha", ledger.Entry{ID: "T1.1", Session: "t1-1"},
			session.CleanOptions{DeleteBranch: true, ConfirmedUnmerged: false}},
		{"no entry at all", ledger.Entry{},
			session.CleanOptions{DeleteBranch: true, ConfirmedUnmerged: false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := daemonCleanOptions(tt.entry); got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

// daemonCleanGit runs git in dir for TestDaemonClean.
func daemonCleanGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestDaemonClean runs the loop's Clean over real git in temp folders (the
// precedent is internal/session's clean tests: a wrong git call cannot be
// caught by a fake). It touches no real session: the repository, the
// worktree and the archive folder are all made here, there is no host, and
// git-spice is stubbed out.
//
// The session's branch holds a commit the base does not, which is exactly
// how a squash-merged branch looks locally.
//
// Load-bearing:
//   - "merged on GitHub" deletes that branch; "not known to be merged" keeps
//     it. Together they are the whole reason the ledger is read.
//   - "no such session" is success with nothing touched: the loop cleans
//     before it marks the task done, so after a kill it asks again.
//   - "ledger unreadable" still cleans, with the cautious options.
func TestDaemonClean(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	tests := []struct {
		name       string
		session    string
		entry      *ledger.Entry
		ledgerErr  error
		wantGone   bool // the worktree and the session's files
		wantBranch bool // the branch t1-1 still exists
	}{
		{name: "merged on GitHub", session: "t1-1",
			entry:    &ledger.Entry{ID: "T1.1", Status: ledger.Shipping, Session: "t1-1", Branch: "t1-1", MergedSHA: "abc123"},
			wantGone: true, wantBranch: false},
		{name: "not known to be merged", session: "t1-1",
			entry:    &ledger.Entry{ID: "T1.1", Status: ledger.Shipping, Session: "t1-1", Branch: "t1-1"},
			wantGone: true, wantBranch: true},
		{name: "ledger unreadable", session: "t1-1", ledgerErr: errors.New("ledger file is damaged"),
			wantGone: true, wantBranch: true},
		{name: "no such session", session: "t9-9",
			entry:    &ledger.Entry{ID: "T9.9", Status: ledger.Shipping, Session: "t9-9", Branch: "t9-9", MergedSHA: "abc123"},
			wantGone: false, wantBranch: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			root, wt, home := filepath.Join(base, "repo"), filepath.Join(base, "t1-1"), filepath.Join(base, "home")
			for _, d := range []string{root, home} {
				if err := os.Mkdir(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			daemonCleanGit(t, root, "init", "-b", "main")
			daemonCleanGit(t, root, "config", "user.name", "afk Test")
			daemonCleanGit(t, root, "config", "user.email", "afk-test@example.com")
			writeBuildFile(t, root, ".gitignore", ".afk/\n")
			daemonCleanGit(t, root, "add", "-A")
			daemonCleanGit(t, root, "commit", "-m", "chore: Initial commit")
			daemonCleanGit(t, root, "worktree", "add", "-b", "t1-1", wt, "main")
			writeBuildFile(t, wt, "greeting.txt", "hello\n")
			daemonCleanGit(t, wt, "add", "-A")
			daemonCleanGit(t, wt, "commit", "-m", "feat: Add a greeting")
			writeBuildFile(t, wt, ".afk/t1-1/status.json", `{"session_id":"11111111-1111-1111-1111-111111111111","plan_name":"t1-1","phase":"done","branch":"t1-1","base_branch":"main","loop_count":0,"awaiting_input":false,"tmux_window":"","updated_at":"2026-10-11T09:00:00Z"}`+"\n")

			var out bytes.Buffer
			cd := session.DefaultCleanDeps()
			cd.Stdout, cd.Stderr = &out, &out
			cd.HomeDir = func() (string, error) { return home, nil }
			cd.Now = func() time.Time { return daemonTestNow }
			cd.IsSpiceTracked = func(string, string) bool { return false }
			clean := daemonClean(cd, func(got string) (ledger.Ledger, error) {
				if got != root {
					t.Errorf("ledger loaded from %q, want the project root", got)
				}
				if tt.ledgerErr != nil {
					return ledger.Ledger{}, tt.ledgerErr
				}
				return ledger.Ledger{Tasks: []ledger.Entry{{ID: "T0.9", Status: ledger.Done, Session: "t0-9"}, *tt.entry}}, nil
			})
			if err := clean(daemon.Project{Name: "demo", Root: root}, tt.session); err != nil {
				t.Fatalf("clean: %v\n%s", err, out.String())
			}
			_, statErr := os.Stat(wt)
			if gone := os.IsNotExist(statErr); gone != tt.wantGone {
				t.Errorf("worktree gone = %v, want %v\n%s", gone, tt.wantGone, out.String())
			}
			branches := daemonCleanGit(t, root, "branch", "--list", "t1-1")
			if has := branches != ""; has != tt.wantBranch {
				t.Errorf("branch t1-1 exists = %v, want %v\n%s", has, tt.wantBranch, out.String())
			}
		})
	}

	t.Run("sessions cannot be listed", func(t *testing.T) {
		cd := session.DefaultCleanDeps()
		cd.Run = func(string, string, ...string) (string, error) { return "", errors.New("git is broken") }
		clean := daemonClean(cd, func(string) (ledger.Ledger, error) { return ledger.Ledger{}, nil })
		if err := clean(daemon.Project{Name: "demo", Root: t.TempDir()}, "t1-1"); err == nil {
			t.Error("got no error; a listing that failed is not \"no such session\"")
		}
	})
}

// TestDaemonStoppedLineText pins the report's line for a stopped line: after which
// task, why, and the command that clears it. The damaged-marker row matters
// because a marker nobody can read still stops the line, and the owner must
// still be told how to clear it.
func TestDaemonStoppedLineText(t *testing.T) {
	tests := []struct {
		name string
		in   daemonProjectStatus
		want string
	}{
		{"task and reason", daemonProjectStatus{Name: "demo", Stopped: true, StopTask: "T1.1", StopReason: "the deploy did not come up healthy within 15m0s."},
			`Stopped: the line is stopped after T1.1: the deploy did not come up healthy within 15m0s. No new task will start until you run "afk resume demo", which clears the stop.`},
		{"task only", daemonProjectStatus{Name: "demo", Stopped: true, StopTask: "T1.1"},
			`Stopped: the line is stopped after T1.1. No new task will start until you run "afk resume demo", which clears the stop.`},
		{"damaged marker", daemonProjectStatus{Name: "demo", Stopped: true},
			`Stopped: the line is stopped. No new task will start until you run "afk resume demo", which clears the stop.`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stoppedLineText(tt.in); got != tt.want {
				t.Errorf("got  %q\nwant %q", got, tt.want)
			}
		})
	}
}

// TestDaemonTaskRunning pins which states get a "Running" line. Load-bearing:
// the shipping and stopped-line rows name a session and must still be false,
// since that session's work is over and a running time would read as
// progress; and every kind the loop can write has a row, so a new one is
// decided here on purpose.
func TestDaemonTaskRunning(t *testing.T) {
	running := map[string]bool{
		string(daemon.ActLaunch): true, string(daemon.ActCreate): true,
		string(daemon.ActWait): true, string(daemon.ActUnpark): true,
	}
	for _, kind := range daemon.Kinds() {
		t.Run(kind, func(t *testing.T) {
			if got := daemonTaskRunning(daemonstate.State{Kind: kind, Task: "T1.1", Session: "t1-1"}); got != running[kind] {
				t.Errorf("with a session: got %v, want %v", got, running[kind])
			}
			if daemonTaskRunning(daemonstate.State{Kind: kind, Task: "T1.1"}) {
				t.Error("with no session: got true, want false")
			}
		})
	}
}
