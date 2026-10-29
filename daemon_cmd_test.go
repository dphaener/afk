package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
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
	"github.com/dphaener/afk/internal/decisionlog"
	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/governor"
	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/masterplan"
	"github.com/dphaener/afk/internal/planlint"
	"github.com/dphaener/afk/internal/planpatch"
	"github.com/dphaener/afk/internal/projectconfig"
	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/rex"
	"github.com/dphaener/afk/internal/session"
	"github.com/dphaener/afk/internal/ship"
	"github.com/dphaener/afk/internal/usagestate"
	"github.com/dphaener/afk/internal/webpass"
	"github.com/dphaener/afk/internal/webpush"
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
	// governors is the [governor] table each newDeps call was handed.
	governors []daemonconfig.Governor
	alive     bool
	ctx       context.Context
	// common maps a folder to the git common dir the fake git reports.
	common map[string]string
	// roots are the projects made by project(), in order.
	roots []string
	// binary is what the fake "afk's own program file" read answers, given
	// how many times it has been asked (1 is the read at the start). Nil
	// answers the same identity every time. binaryReads counts the asks.
	binary      func(read int) (daemon.Binary, error)
	binaryReads int
	// stderr and stdout are what the last runDaemon wrote to them, for a
	// row's check.
	stderr, stdout string
	// listens are the addresses the fake "open the web page's port" was asked
	// for, and listeners what it handed back, in order. listenErr, when set,
	// is its answer instead. No row opens a real port.
	listens   []string
	listeners []*pipeListener
	listenErr error
	// launched, when a test sets it, is sent every launch as well, so a test
	// that runs the daemon on a goroutine of its own can wait for a launch
	// without reading launches while the loop may be writing it.
	launched chan daemon.LaunchRequest
}

// daemonTestBinary is the program file the rig reports unless a test says
// otherwise.
var daemonTestBinary = daemon.Binary{Path: "/opt/afk/bin/afk", Size: 100, ModTime: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}

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
	r.roots = append(r.roots, root)
	return root
}

// restartLines counts the daemon_restart lines in a project's decision log.
func restartLines(t *testing.T, root string) int {
	t.Helper()
	entries, err := decisionlog.Read(decisionlog.Path(root))
	if err != nil {
		t.Fatalf("reading the decision log of %s: %v", root, err)
	}
	n := 0
	for _, e := range entries {
		if e.Kind == daemon.KindDaemonRestart {
			n++
		}
	}
	return n
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
		newDeps: func(p provider.Provider, providerName string, gov daemonconfig.Governor, _ func() []daemon.Project, log func(string, ...any)) daemon.Deps {
			r.providers = append(r.providers, providerName)
			r.governors = append(r.governors, gov)
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
					if r.launched != nil {
						r.launched <- req
					}
					return nil
				},
				RecordTokens: func(daemon.Project, string, string, string) (int, error) { return 0, nil },
				Log:          log,
			}
		},
		signalContext: func() (context.Context, context.CancelFunc) { return r.ctx, func() {} },
		binary: func() (daemon.Binary, error) {
			r.binaryReads++
			if r.binary == nil {
				return daemonTestBinary, nil
			}
			return r.binary(r.binaryReads)
		},
		listen: func(addr string) (net.Listener, error) {
			r.listens = append(r.listens, addr)
			if r.listenErr != nil {
				return nil, r.listenErr
			}
			ln := newPipeListener()
			r.listeners = append(r.listeners, ln)
			return ln, nil
		},
	}
}

// TestRunDaemon runs the command body against temp folders and a fake loop.
//
// Load-bearing rows:
//   - "once, no projects" is exit 0 with a line that says there is nothing to
//     look at: an empty list is how every machine starts, not a mistake.
//   - "no projects: the daemon starts and waits" is the other half of that. It
//     must go on looking (three passes are counted) and must not exit, or a
//     service manager would restart it in a loop until the first plan is
//     approved. It says so once, not once per pass.
//   - "the config becomes unreadable while the daemon runs" must keep building
//     the project it had: the launch in it happens on a pass whose read of the
//     config failed. The problem is logged once although two passes hit it.
//   - "a setting other than the project list is not picked up" pins that the
//     reload is the list and nothing else: the tick written mid-run is an hour,
//     and the passes after it still come a second apart.
//   - "paused project starts nothing" is the manual check's safety property:
//     it is what makes `afk daemon --once` safe to try on a real project.
//   - "already running" must not launch: two loops over one ledger would each
//     believe it runs one task at a time.
//   - "worktree path" pins that a project path naming a session's worktree
//     is run from the main checkout, where the ledger and the inbox live.
//   - "stops when asked" pins exit 0 on a stop signal; a service manager
//     reads anything else as a crash and restarts the daemon.
//   - "program file changed after the first pass" is the whole of "pick up
//     new code" (PLAN T2.2a): exit 0, the new-version line and not the
//     ordinary one, one daemon_restart line in every project's decision log,
//     and the first pass done in full before it. It is the one row that waits
//     for a real tick (1s, the shortest the config allows).
//   - "program file unreadable at the start" must still run, and must leave
//     the note where status finds it: a daemon that silently never restarts
//     would bring the hand work back without saying so.
//   - "once never looks at the program file" has a file that would read as
//     changed on every look, so any look at all would show.
func TestRunDaemon(t *testing.T) {
	const newVersionLine = "afk daemon found a new version of itself on disk and is stopping so it can be started again. Every session is still running."
	const stoppedLine = "afk daemon stopped. Every session is still running; the next start picks them up."
	// pausedProjects writes a config naming n paused projects, so a pass has
	// something to look at and nothing to start.
	pausedProjects := func(r *daemonRig, n int) {
		cfg := "[daemon]\ntick = \"1s\"\n"
		for i := 0; i < n; i++ {
			root := r.project()
			if err := daemonstate.Pause(root, daemonTestNow); err != nil {
				r.t.Fatal(err)
			}
			cfg += fmt.Sprintf("[[project]]\nname = \"p%d\"\npath = %q\n", i, root)
		}
		r.writeConfig(cfg)
	}
	tests := []struct {
		name  string
		args  []string
		setup func(r *daemonRig)
		code  int
		// out and errOut are words stdout and stderr must hold; errNot are
		// words stderr must not.
		out, errOut []string
		errNot      []string
		launches    int
		check       func(t *testing.T, r *daemonRig)
	}{
		{
			name:   "once, no projects",
			args:   []string{"--once"},
			code:   0,
			out:    []string{"No projects yet, so there is nothing to look at", "afk project add", "afk plan approve"},
			errNot: []string{"afk daemon started", "without a restart"},
			check: func(t *testing.T, r *daemonRig) {
				if r.binaryReads != 0 {
					t.Errorf("--once read the program file %d times, want never", r.binaryReads)
				}
			},
		},
		{
			name: "no projects: the daemon starts and waits",
			setup: func(r *daemonRig) {
				r.writeConfig("[daemon]\ntick = \"1s\"\n")
				// Asked to stop at the fourth look at the program file, which
				// is the top of the third pass: two whole passes over nothing
				// have then run. The limit only keeps a daemon that stopped
				// looking from hanging the suite.
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				r.t.Cleanup(cancel)
				r.ctx = ctx
				r.binary = func(read int) (daemon.Binary, error) {
					if read >= 4 {
						cancel()
					}
					return daemonTestBinary, nil
				}
			},
			code: 0,
			errOut: []string{"afk daemon started (pid 4242): 0 project(s), looking every 1s",
				"No projects yet. Add one with afk project add, or approve a plan with afk plan approve; this daemon picks it up without a restart.",
				"read again on every look", "[governor]", "[web]", "provider", "restart afk daemon after changing any of them", stoppedLine},
			errNot: []string{"no projects are configured"},
			check: func(t *testing.T, r *daemonRig) {
				if errors.Is(r.ctx.Err(), context.DeadlineExceeded) {
					t.Fatal("the run ended on the test's time limit: a daemon with no projects stopped looking")
				}
				if r.binaryReads != 4 {
					t.Errorf("the program file was read %d times, want 4 (the start and three passes)", r.binaryReads)
				}
				if n := strings.Count(r.stderr, "No projects yet"); n != 1 {
					t.Errorf("the log says there are no projects %d times, want once", n)
				}
			},
		},
		{
			name: "the config becomes unreadable while the daemon runs",
			setup: func(r *daemonRig) {
				root := r.project()
				if err := daemonstate.Pause(root, daemonTestNow); err != nil {
					r.t.Fatal(err)
				}
				r.writeConfig("[daemon]\ntick = \"1s\"\n[[project]]\nname = \"demo\"\npath = \"" + root + "\"\n")
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				r.t.Cleanup(cancel)
				r.ctx = ctx
				// Each look at the program file comes straight after that
				// pass's read of the config, and before the pass looks at any
				// project.
				r.binary = func(read int) (daemon.Binary, error) {
					switch read {
					case 2:
						// First pass: the config was read. Break it now; this
						// pass still finds the project paused.
						r.writeConfig("[[project]\npath = half a line")
					case 3:
						// Second pass: its read of the config has just failed.
						// Resume the project, so the only way a task starts is
						// this pass looking at a project it could not re-read.
						if _, err := daemonstate.Resume(root); err != nil {
							r.t.Error(err)
						}
					case 4:
						// Third pass: a second failed read, then stop.
						cancel()
					}
					return daemonTestBinary, nil
				}
			},
			code:     0,
			errOut:   []string{"afk: the project list could not be read, so the projects already running carry on:", "config.toml", stoppedLine},
			errNot:   []string{"no longer running", "No projects yet"},
			launches: 1,
			check: func(t *testing.T, r *daemonRig) {
				if errors.Is(r.ctx.Err(), context.DeadlineExceeded) {
					t.Fatal("the run ended on the test's time limit")
				}
				if got := r.launches[0]; got.Task != "T1.1" || got.Project.Root != r.roots[0] || got.Project.Name != "demo" {
					t.Errorf("launch = %+v, want T1.1 for demo at %s", got, r.roots[0])
				}
				if n := strings.Count(r.stderr, "the project list could not be read"); n != 1 {
					t.Errorf("the config problem is logged %d times, want once (two passes hit it)", n)
				}
			},
		},
		{
			name: "a setting other than the project list is not picked up",
			setup: func(r *daemonRig) {
				root := r.project()
				if err := daemonstate.Pause(root, daemonTestNow); err != nil {
					r.t.Fatal(err)
				}
				project := "[[project]]\nname = \"demo\"\npath = \"" + root + "\"\n"
				r.writeConfig("[daemon]\ntick = \"1s\"\n" + project)
				// Far below the hour the new tick asks for, far above the two
				// seconds two passes need.
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				r.t.Cleanup(cancel)
				r.ctx = ctx
				r.binary = func(read int) (daemon.Binary, error) {
					switch read {
					case 2:
						r.writeConfig("provider = \"no-such-agent\"\n[daemon]\ntick = \"1h\"\n" + project)
					case 4:
						cancel()
					}
					return daemonTestBinary, nil
				}
			},
			code:   0,
			errOut: []string{"looking every 1s", stoppedLine},
			errNot: []string{"could not be read", "no-such-agent", "no longer running"},
			check: func(t *testing.T, r *daemonRig) {
				if errors.Is(r.ctx.Err(), context.DeadlineExceeded) {
					t.Fatal("the run ended on the test's time limit: the daemon took the new tick of an hour from a config it re-read")
				}
				if len(r.providers) != 1 {
					t.Errorf("the loop's deps were built %d times, want once", len(r.providers))
				}
			},
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
			// The plan's failure check: a limit that could never work stops
			// the daemon before it looks at any project.
			name: "a governor limit that is out of range",
			args: []string{"--once"},
			setup: func(r *daemonRig) {
				r.writeConfig("[governor]\npause_at = 3\n[[project]]\npath = \"" + r.project() + "\"\n")
			},
			code:   1,
			errOut: []string{"config.toml", "governor.pause_at"},
			check: func(t *testing.T, r *daemonRig) {
				if len(r.governors) != 0 {
					t.Error("the loop's deps were built for a config that does not load")
				}
			},
		},
		{
			// Load-bearing: the limits the owner wrote are the ones the real
			// loop is built with, and the keys left out carry the defaults.
			name: "the governor table reaches the loop",
			args: []string{"--once"},
			setup: func(r *daemonRig) {
				root := r.project()
				if err := daemonstate.Pause(root, daemonTestNow); err != nil {
					r.t.Fatal(err)
				}
				r.writeConfig("[governor]\npause_at = 80\nlaunch_below = 50\nprobe_interval = \"2m\"\non_unknown_usage = \"hold\"\n[[project]]\nname = \"p0\"\npath = \"" + root + "\"\n")
			},
			code: 0,
			out:  []string{"p0: waiting"},
			check: func(t *testing.T, r *daemonRig) {
				want := daemonconfig.Governor{PauseAt: 80, LaunchBelow: 50, StaleAfter: 30 * time.Minute,
					ProbeInterval: 2 * time.Minute, OnUnknownUsage: governor.UnknownHold}
				if len(r.governors) != 1 || r.governors[0] != want {
					t.Errorf("newDeps got %+v, want one call with %+v", r.governors, want)
				}
			},
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
		{
			name:   "program file unchanged",
			setup:  func(r *daemonRig) { pausedProjects(r, 2) },
			code:   0,
			errOut: []string{stoppedLine},
			errNot: []string{"found a new version", "cannot see its own program file"},
			check: func(t *testing.T, r *daemonRig) {
				// The read at the start and the one at the top of the only
				// pass, which then finds it has been asked to stop.
				if r.binaryReads != 2 {
					t.Errorf("the program file was read %d times, want 2", r.binaryReads)
				}
				for _, root := range r.roots {
					if n := restartLines(t, root); n != 0 {
						t.Errorf("%s has %d daemon_restart lines, want none", root, n)
					}
				}
				if daemonstate.BinaryUnknown(r.stateDir()) {
					t.Error("running.json says the program file is unknown, but it was read")
				}
			},
		},
		{
			name: "program file changed after the first pass",
			setup: func(r *daemonRig) {
				pausedProjects(r, 2)
				// Not asked to stop: only the new file ends this run. The
				// limit is there so a broken check fails the test instead of
				// hanging it.
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				r.t.Cleanup(cancel)
				r.ctx = ctx
				r.binary = func(read int) (daemon.Binary, error) {
					b := daemonTestBinary
					if read >= 3 { // 1 the start, 2 the first pass, 3 the second
						b.Size++
					}
					return b, nil
				}
			},
			code:   0,
			errOut: []string{newVersionLine},
			errNot: []string{stoppedLine},
			check: func(t *testing.T, r *daemonRig) {
				if r.ctx.Err() != nil {
					t.Fatal("the run ended on the test's time limit, not on the new program file")
				}
				if r.binaryReads != 3 {
					t.Errorf("the program file was read %d times, want 3", r.binaryReads)
				}
				for _, root := range r.roots {
					if n := restartLines(t, root); n != 1 {
						t.Errorf("%s has %d daemon_restart lines, want exactly 1", root, n)
					}
					if _, ok := daemonstate.ReadState(root); !ok {
						t.Errorf("%s was never looked at: the first pass must finish before the daemon stops", root)
					}
				}
			},
		},
		{
			name: "program file unreadable at the start",
			setup: func(r *daemonRig) {
				pausedProjects(r, 1)
				// The file stays unreadable. The daemon is asked to stop at
				// the third look (the top of its second pass), so one whole
				// pass runs first and the looks after the start are seen to
				// carry on without a word. The time limit only keeps a daemon
				// that never looks again from hanging the test.
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				r.t.Cleanup(cancel)
				r.ctx = ctx
				r.binary = func(read int) (daemon.Binary, error) {
					if read >= 3 {
						cancel()
					}
					return daemon.Binary{}, errors.New("no such file")
				}
			},
			code:   0,
			errOut: []string{"afk daemon started", "afk cannot see its own program file", "Restart it by hand after an install", "no such file", stoppedLine},
			errNot: []string{"found a new version"},
			check: func(t *testing.T, r *daemonRig) {
				if n := strings.Count(r.stderr, "cannot see its own program file"); n != 1 {
					t.Errorf("the log says the program file cannot be seen %d times, want once (at the start only)", n)
				}
				if !daemonstate.BinaryUnknown(r.stateDir()) {
					t.Error("running.json does not say binary_unknown")
				}
				if errors.Is(r.ctx.Err(), context.DeadlineExceeded) {
					t.Fatal("the run ended on the test's time limit: the daemon stopped looking for its program file")
				}
				if r.binaryReads != 3 {
					t.Errorf("the program file was looked for %d times, want 3: it is looked for again on every pass", r.binaryReads)
				}
				if pid, _, ok := daemonstate.ReadMachine(r.stateDir()); !ok || pid != 4242 {
					t.Errorf("machine file = pid %d ok %v after the mark, want pid 4242", pid, ok)
				}
				if _, ok := daemonstate.ReadState(r.roots[0]); !ok {
					t.Error("the daemon did not look at the project: an unreadable program file must not stop it")
				}
				if n := restartLines(t, r.roots[0]); n != 0 {
					t.Errorf("%d daemon_restart lines, want none", n)
				}
				const line = "afk cannot see its own program file, so it will not restart by itself when a new version is installed. Restart it by hand after an install."
				status := func(alive bool) string {
					r.alive = alive
					var so, se bytes.Buffer
					if code := runDaemon([]string{"status"}, &so, &se, r.env()); code != 0 {
						t.Fatalf("status exit %d: %s", code, se.String())
					}
					return so.String()
				}
				running := status(true)
				t.Logf("status while that daemon runs:\n%s", running)
				if n := strings.Count(running, line); n != 1 {
					t.Errorf("status shows the line %d times while the daemon runs, want once", n)
				}
				if gone := status(false); strings.Contains(gone, "cannot see") {
					t.Errorf("status shows the line for a daemon that is not running:\n%s", gone)
				}
			},
		},
		{
			name: "once never looks at the program file",
			args: []string{"--once"},
			setup: func(r *daemonRig) {
				pausedProjects(r, 2)
				r.binary = func(read int) (daemon.Binary, error) {
					b := daemonTestBinary
					b.Size += int64(read) // different on every look
					return b, nil
				}
			},
			code:   0,
			out:    []string{"p0: waiting", "p1: waiting"},
			errNot: []string{"found a new version", "cannot see"},
			check: func(t *testing.T, r *daemonRig) {
				if r.binaryReads != 0 {
					t.Errorf("--once read the program file %d times, want never", r.binaryReads)
				}
				for _, root := range r.roots {
					if n := restartLines(t, root); n != 0 {
						t.Errorf("%s has %d daemon_restart lines, want none", root, n)
					}
				}
				if daemonstate.BinaryUnknown(r.stateDir()) {
					t.Error("--once marked the program file unknown")
				}
			},
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
			r.stderr = se.String()
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
			for _, w := range tt.errNot {
				if strings.Contains(se.String(), w) {
					t.Errorf("stderr holds %q, which it must not", w)
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

// daemonTestWebPassword is the password the web rows set, and
// daemonTestWebRecord its saved record. The record is made once with few
// iterations and a fixed salt, so every run is the same and no row pays for
// the real iteration count.
const daemonTestWebPassword = "pw-3f9a1c7e5b2d4a68"

var (
	daemonTestWebRecord    = mustDaemonTestRecord(daemonTestWebPassword, 5)
	daemonTestOldWebRecord = mustDaemonTestRecord("pw-old-0000000000000000", 6)
)

func mustDaemonTestRecord(password string, saltByte byte) webpass.Record {
	rec, err := webpass.New(password, bytes.NewReader(bytes.Repeat([]byte{saltByte}, 16)), 1000)
	if err != nil {
		panic(err)
	}
	return rec
}

// daemonTestWebCookie is the sign-in cookie value a browser holds after
// signing in with daemonTestWebPassword, and daemonTestOldWebCookie one kept
// from before a password change. Root-package tests prove themselves with
// these, never with a header: the page has no other way in.
func daemonTestWebCookie() string    { return daemonTestWebRecord.CookieValue() }
func daemonTestOldWebCookie() string { return daemonTestOldWebRecord.CookieValue() }

// setWebPassword saves daemonTestWebRecord in the rig's state folder, as
// `afk web password` would.
func (r *daemonRig) setWebPassword() {
	r.t.Helper()
	if err := webpass.Write(r.env().state, daemonTestWebRecord); err != nil {
		r.t.Fatal(err)
	}
}

// daemonTestLeftoverToken is a [web] token left from before the page used a
// password. It is unlike anything else a row prints, so "the token is in
// the output" cannot match by accident.
const daemonTestLeftoverToken = "tok-3f9a1c7e5b2d4a68"

// TestRunDaemonWeb runs `afk daemon` with a [web] table against the rig. The
// port is the rig's pipe listener: nothing binds, and a request to the page
// travels over an in-memory connection.
//
// Load-bearing rows:
//   - "password set" is the feature seen from the command: the port asked for
//     is the configured bind, the page answers while the loop runs (the
//     sign-in form at / and a 401 without the cookie, 200 with it), and the
//     listener is closed once the command returns.
//   - "the port cannot be opened" must be exit 1 with the bind and the config
//     file named, before the loop looks at any project, and must give the
//     machine lock back: a daemon that kept it would lock out the next start.
//   - "a reachable bind, no password" (for `afk daemon` and `--once` alike) is
//     the non-loopback refusal seen from the command: exit 1, the message
//     names web.bind, the password file and `afk web password`, and nothing
//     is opened, built or looked at. It moved here from daemonconfig.Load, so
//     without these rows a reachable page with no password would just start.
//   - "no password on loopback", "--once" and "status" must never ask for a
//     port. With no password nobody could sign in, and the other two are over
//     before anyone could open a page.
//   - the leftover-token rows: the line is logged once, the page is still on
//     or off by the password alone, and the token's value is never printed.
//     The --once one pins that webStart's lines are not printed for a single
//     pass.
//   - "a damaged password file" is logged with the file's path and is off on
//     loopback: the daemon still builds, and the owner is told which file.
//   - "a project added while the daemon runs gets a page" is the reload seen
//     from the page: 404 before the project is in config.toml, 200 for the
//     project and for its plan on the pass after, from the same daemon.
//   - "stopping for a new version closes the page": that way out returns
//     without the stop signal, so a shutdown tied to the signal would leave
//     the port held against the daemon that is started next.
//
// Every row also checks that nothing printed holds the password, its cookie
// value or the leftover token.
func TestRunDaemonWeb(t *testing.T) {
	const bind = "127.0.0.1:4999"
	// oneProject writes a config with one paused project (so a pass has
	// something to look at and nothing to start) and the given [web] table.
	oneProject := func(r *daemonRig, webTable string) {
		root := r.project()
		if err := daemonstate.Pause(root, daemonTestNow); err != nil {
			r.t.Fatal(err)
		}
		r.writeConfig(fmt.Sprintf("[daemon]\ntick = \"1s\"\n%s\n[[project]]\nname = \"demo\"\npath = %q\n", webTable, root))
	}
	withWeb := fmt.Sprintf("[web]\nbind = %q\npublic_url = \"https://afk.example/\"\n", bind)
	withLeftover := fmt.Sprintf("[web]\nbind = %q\ntoken = %q\npublic_url = \"https://afk.example/\"\n", bind, daemonTestLeftoverToken)
	reachable := "[web]\nbind = \"0.0.0.0:4999\"\n"
	const leftoverLine = "web page: it no longer uses a token"
	const offLine = "web page: off (run `afk web password` to turn it on)"
	// running gives the row a daemon that is not asked to stop at once. The
	// limit only keeps a broken row from hanging the suite.
	running := func(r *daemonRig) context.CancelFunc {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		r.t.Cleanup(cancel)
		r.ctx = ctx
		return cancel
	}
	passwordPath := func(r *daemonRig) string {
		path, err := webpass.Path(r.env().state)
		if err != nil {
			r.t.Fatal(err)
		}
		return path
	}
	// damage writes a password file that is not a record.
	damage := func(r *daemonRig) {
		path := passwordPath(r)
		writeBuildFile(r.t, filepath.Dir(path), filepath.Base(path), "not a record {")
	}
	// refused is the check every refusal row shares: the files are named,
	// and nothing was built, locked or looked at.
	refused := func(t *testing.T, r *daemonRig) {
		for _, w := range []string{r.configPath(), passwordPath(r)} {
			if !strings.Contains(r.stderr, w) {
				t.Errorf("the message does not name %s:\n%s", w, r.stderr)
			}
		}
		if len(r.governors) != 0 {
			t.Error("the loop's deps were built for a config that is refused")
		}
		if _, ok := daemonstate.ReadState(r.roots[0]); ok {
			t.Error("the loop looked at the project under a config that was refused")
		}
		if pid, _, ok := daemonstate.ReadMachine(r.stateDir()); ok {
			t.Errorf("the machine lock was taken (pid %d) for a refused start", pid)
		}
	}
	tests := []struct {
		name        string
		args        []string
		setup       func(r *daemonRig)
		code        int
		out, errOut []string
		errNot      []string
		listens     []string
		check       func(t *testing.T, r *daemonRig)
	}{
		{
			name: "password set: the page is opened, answers while the loop runs, and is closed after",
			setup: func(r *daemonRig) {
				oneProject(r, withWeb)
				r.setWebPassword()
				cancel := running(r)
				// The second look at the program file is the top of the first
				// pass: the loop is running and the page must be up. The row
				// asks it from here and then asks the daemon to stop.
				r.binary = func(read int) (daemon.Binary, error) {
					if read == 2 {
						ln := r.listeners[0]
						code, body, err := ln.fetch("/", "")
						if err != nil {
							r.t.Fatalf("GET /: %v", err)
						}
						if code != http.StatusUnauthorized || !strings.Contains(body, `action="/signin"`) {
							r.t.Errorf("GET / without the cookie answered %d, want 401 with the sign-in form:\n%s", code, body)
						}
						if code := ln.get(r.t, "/api/usage", ""); code != http.StatusUnauthorized {
							r.t.Errorf("the page answered %d without the cookie, want 401", code)
						}
						if code := ln.get(r.t, "/api/usage", daemonTestOldWebCookie()); code != http.StatusUnauthorized {
							r.t.Errorf("the page answered %d to a cookie from an old password, want 401", code)
						}
						if code := ln.get(r.t, "/api/usage", daemonTestWebCookie()); code != http.StatusOK {
							r.t.Errorf("the page answered %d with the cookie, want 200", code)
						}
						cancel()
					}
					return daemonTestBinary, nil
				}
			},
			code:    0,
			errOut:  []string{"web page: listening on http://" + bind, "afk daemon started", "afk daemon stopped"},
			errNot:  []string{"web page: off", "stopped answering", leftoverLine},
			listens: []string{bind},
			check: func(t *testing.T, r *daemonRig) {
				if r.binaryReads < 2 {
					t.Fatal("the loop never ran, so the page was never asked")
				}
				if !r.listeners[0].isClosed() {
					t.Error("the listener is still open after the daemon returned")
				}
			},
		},
		{
			name: "the port cannot be opened",
			setup: func(r *daemonRig) {
				oneProject(r, withWeb)
				r.setWebPassword()
				running(r)
				r.listenErr = errors.New("listen tcp " + bind + ": bind: address already in use")
			},
			code:    1,
			errOut:  []string{"could not open the web page on " + bind, "address already in use", "change [web] bind in", "config.toml"},
			errNot:  []string{"afk daemon started", "web page: listening"},
			listens: []string{bind},
			check: func(t *testing.T, r *daemonRig) {
				if !strings.Contains(r.stderr, r.configPath()) {
					t.Errorf("the message does not name the config file %s:\n%s", r.configPath(), r.stderr)
				}
				if r.ctx.Err() != nil {
					t.Fatal("the run ended on the test's time limit: the loop ran with no page")
				}
				if _, ok := daemonstate.ReadState(r.roots[0]); ok {
					t.Error("the loop looked at the project although the page could not be opened")
				}
				// The lock was given back: the next start, with the port
				// free again, gets it.
				r.listenErr = nil
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				r.ctx = ctx
				var so, se bytes.Buffer
				if code := runDaemon(nil, &so, &se, r.env()); code != 0 {
					t.Errorf("the next start exited %d, want 0 (was the machine lock kept?): %s", code, se.String())
				}
			},
		},
		{
			name:   "no password on loopback: the page is off and no port is asked for",
			setup:  func(r *daemonRig) { oneProject(r, withWeb) },
			code:   0,
			errOut: []string{offLine, "afk daemon started"},
			errNot: []string{"web page: listening", leftoverLine},
			check: func(t *testing.T, r *daemonRig) {
				if n := strings.Count(r.stderr, "web page: off"); n != 1 {
					t.Errorf("the log says the page is off %d times, want once", n)
				}
			},
		},
		{
			name:   "no web table at all",
			setup:  func(r *daemonRig) { oneProject(r, "") },
			code:   0,
			errOut: []string{offLine},
		},
		{
			name: "a leftover token with a password: logged, ignored, and the page is on",
			setup: func(r *daemonRig) {
				oneProject(r, withLeftover)
				r.setWebPassword()
				cancel := running(r)
				r.binary = func(read int) (daemon.Binary, error) {
					if read == 2 {
						if code := r.listeners[0].get(r.t, "/api/usage", daemonTestWebCookie()); code != http.StatusOK {
							r.t.Errorf("the page answered %d with the cookie, want 200", code)
						}
						cancel()
					}
					return daemonTestBinary, nil
				}
			},
			code:    0,
			errOut:  []string{leftoverLine, "Remove the token line from [web]", "config.toml", "web page: listening on http://" + bind},
			errNot:  []string{"web page: off"},
			listens: []string{bind},
			check: func(t *testing.T, r *daemonRig) {
				if n := strings.Count(r.stderr, leftoverLine); n != 1 {
					t.Errorf("the leftover token is logged %d times, want once", n)
				}
			},
		},
		{
			name:   "a leftover token with no password: logged, and the page is off",
			setup:  func(r *daemonRig) { oneProject(r, withLeftover) },
			code:   0,
			errOut: []string{leftoverLine, offLine},
			errNot: []string{"web page: listening"},
		},
		{
			name: "a damaged password file: logged with its path, page off on loopback",
			setup: func(r *daemonRig) {
				oneProject(r, withWeb)
				damage(r)
			},
			code:   0,
			errOut: []string{"web page: off, because the password file cannot be read", "afk web password", "afk daemon started"},
			errNot: []string{"web page: listening", "not a record {"},
			check: func(t *testing.T, r *daemonRig) {
				if !strings.Contains(r.stderr, passwordPath(r)) {
					t.Errorf("the log does not name the password file %s:\n%s", passwordPath(r), r.stderr)
				}
			},
		},
		{
			name: "--once with a password never opens the page",
			args: []string{"--once"},
			setup: func(r *daemonRig) {
				oneProject(r, withWeb)
				r.setWebPassword()
			},
			code:   0,
			out:    []string{"demo: waiting"},
			errNot: []string{"web page"},
		},
		{
			name:   "--once prints no page line, leftover token or not",
			args:   []string{"--once"},
			setup:  func(r *daemonRig) { oneProject(r, withLeftover) },
			code:   0,
			out:    []string{"demo: waiting"},
			errNot: []string{"web page", "token"},
		},
		{
			name: "status with a password never opens the page",
			args: []string{"status"},
			setup: func(r *daemonRig) {
				oneProject(r, withLeftover)
				r.setWebPassword()
			},
			code:   0,
			out:    []string{"demo"},
			errNot: []string{"web page"},
		},
		{
			name:   "a reachable bind, no password",
			setup:  func(r *daemonRig) { oneProject(r, reachable) },
			code:   1,
			errOut: []string{"config.toml", "web.bind", "0.0.0.0:4999", "no web password is set", "afk web password"},
			errNot: []string{"afk daemon started", "web page"},
			check:  refused,
		},
		{
			name:   "--once with a reachable bind, no password",
			args:   []string{"--once"},
			setup:  func(r *daemonRig) { oneProject(r, reachable) },
			code:   1,
			errOut: []string{"config.toml", "web.bind", "0.0.0.0:4999", "afk web password"},
			errNot: []string{"web page"},
			check: func(t *testing.T, r *daemonRig) {
				refused(t, r)
				if strings.Contains(r.stdout, "demo") {
					t.Errorf("the single pass ran under a refused config:\n%s", r.stdout)
				}
			},
		},
		{
			name: "a reachable bind with a damaged password file",
			setup: func(r *daemonRig) {
				oneProject(r, reachable)
				damage(r)
			},
			code:   1,
			errOut: []string{"web.bind", "cannot be read", "afk web password"},
			errNot: []string{"afk daemon started", "not a record {"},
			check:  refused,
		},
		{
			// The page as a phone sees it, over the daemon's own listener: a
			// project written into config.toml while the daemon runs has a
			// page one pass later. Nothing here restarts anything.
			name: "a project added while the daemon runs gets a page",
			setup: func(r *daemonRig) {
				r.setWebPassword()
				r.writeConfig(fmt.Sprintf("[daemon]\ntick = \"1s\"\n%s", withWeb))
				cancel := running(r)
				r.binary = func(read int) (daemon.Binary, error) {
					switch read {
					case 2:
						// First pass, the list read as empty.
						if code := r.listeners[0].get(r.t, "/api/p/demo", daemonTestWebCookie()); code != http.StatusNotFound {
							r.t.Errorf("the page answered %d for a project nobody has added, want 404", code)
						}
						oneProject(r, withWeb)
					case 3:
						// Second pass: the list was read again just now.
						for _, path := range []string{"/api/p/demo", "/api/p/demo/plan"} {
							if code := r.listeners[0].get(r.t, path, daemonTestWebCookie()); code != http.StatusOK {
								r.t.Errorf("GET %s answered %d after the project was added, want 200", path, code)
							}
						}
						cancel()
					}
					return daemonTestBinary, nil
				}
			},
			code:    0,
			errOut:  []string{"0 project(s)", "No projects yet", "afk: now running demo", "afk daemon stopped"},
			listens: []string{bind},
			check: func(t *testing.T, r *daemonRig) {
				if r.binaryReads != 3 {
					t.Fatalf("the program file was read %d times, want 3: the second pass never came", r.binaryReads)
				}
			},
		},
		{
			name: "stopping for a new version closes the page",
			setup: func(r *daemonRig) {
				oneProject(r, withWeb)
				r.setWebPassword()
				running(r)
				r.binary = func(read int) (daemon.Binary, error) {
					b := daemonTestBinary
					if read >= 2 { // 1 the start, 2 the top of the first pass
						b.Size++
					}
					return b, nil
				}
			},
			code:    0,
			errOut:  []string{"web page: listening on http://" + bind, "found a new version of itself"},
			listens: []string{bind},
			check: func(t *testing.T, r *daemonRig) {
				if r.ctx.Err() != nil {
					t.Fatal("the run ended on the test's time limit, not on the new program file")
				}
				if !r.listeners[0].isClosed() {
					t.Error("the listener is still open after the daemon stopped for a new version")
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newDaemonRig(t)
			tt.setup(r)
			var so, se bytes.Buffer
			code := runDaemon(tt.args, &so, &se, r.env())
			r.stderr = se.String()
			r.stdout = so.String()
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
			for _, w := range tt.errNot {
				if strings.Contains(se.String(), w) {
					t.Errorf("stderr holds %q, which it must not", w)
				}
			}
			for _, secret := range []string{daemonTestWebPassword, daemonTestWebCookie(), daemonTestLeftoverToken} {
				if strings.Contains(so.String()+se.String(), secret) {
					t.Errorf("the output holds a secret (%q)", secret)
				}
			}
			if !reflect.DeepEqual(r.listens, tt.listens) {
				t.Errorf("ports asked for = %q, want %q", r.listens, tt.listens)
			}
			if len(r.launches) != 0 {
				t.Errorf("launches = %d, want none: the project is paused", len(r.launches))
			}
			if tt.check != nil {
				tt.check(t, r)
			}
		})
	}
}

// TestWebStart is the pure start decision, one row per branch.
//
// Load-bearing rows:
//   - "no password, reachable bind" and "damaged file, reachable bind" are
//     the only refusals between a bind other machines can reach and a page
//     that could let anyone in. They must be errors naming web.bind, the
//     password file and `afk web password`, and must give no log lines (the
//     caller exits before any are printed).
//   - "no password, loopback" is off with the one line that says how to turn
//     it on: the common first run, which must not be an error.
//   - "damaged file, loopback" is off, not on: a page whose password cannot
//     be read can let nobody in, and the line names the file to fix.
//   - the leftover-token rows: one line beside whatever else applies, never
//     an error and never the value, and it changes nothing about on or off.
func TestWebStart(t *testing.T) {
	const cfgPath = "/home/me/.config/afk/config.toml"
	const pwPath = "/home/me/.local/state/afk/web-password.json"
	loop := daemonconfig.Web{Bind: "127.0.0.1:4253"}
	wide := daemonconfig.Web{Bind: "0.0.0.0:4253"}
	leftover := func(w daemonconfig.Web) daemonconfig.Web { w.LeftoverToken = true; return w }
	damaged := fmt.Errorf("the web password file %s is not a valid password record", pwPath)
	unreadable := errors.New("permission denied")
	const offLine = "web page: off (run `afk web password` to turn it on)"
	const leftoverLine = "web page: it no longer uses a token. Remove the token line from [web] in " + cfgPath + ", and run `afk web password` if you have not set a password yet."
	tests := []struct {
		name    string
		web     daemonconfig.Web
		readErr error
		path    string
		on      bool
		logs    []string
		errHas  []string
	}{
		{name: "password set, loopback", web: loop, path: pwPath, on: true},
		{name: "password set, reachable bind", web: wide, path: pwPath, on: true},
		{name: "no password, loopback", web: loop, readErr: webpass.ErrNoPassword, path: pwPath, logs: []string{offLine}},
		{name: "no password, reachable bind", web: wide, readErr: webpass.ErrNoPassword, path: pwPath,
			errHas: []string{cfgPath, "web.bind", "0.0.0.0:4253", "no web password is set in " + pwPath, "afk web password"}},
		{name: "a wrapped no-password error counts as none", web: loop, readErr: fmt.Errorf("x: %w", webpass.ErrNoPassword), path: pwPath, logs: []string{offLine}},
		{name: "damaged file, loopback", web: loop, readErr: damaged, path: pwPath,
			logs: []string{"web page: off, because the password file cannot be read (" + damaged.Error() + "). Run `afk web password` again to set a new password."}},
		{name: "unreadable file names the path it lacks", web: loop, readErr: unreadable, path: pwPath,
			logs: []string{"web page: off, because the password file cannot be read (" + pwPath + ": permission denied). Run `afk web password` again to set a new password."}},
		{name: "damaged file, reachable bind", web: wide, readErr: damaged, path: pwPath,
			errHas: []string{"web.bind", "the web password file " + pwPath + " cannot be read", "afk web password"}},
		{name: "unknown password path", web: wide, readErr: webpass.ErrNoPassword,
			errHas: []string{"web.bind", "afk's state folder (web-password.json)", "afk web password"}},
		{name: "leftover token with a password", web: leftover(loop), path: pwPath, on: true, logs: []string{leftoverLine}},
		{name: "leftover token, no password, loopback", web: leftover(loop), readErr: webpass.ErrNoPassword, path: pwPath,
			logs: []string{leftoverLine, offLine}},
		{name: "leftover token, no password, reachable bind", web: leftover(wide), readErr: webpass.ErrNoPassword, path: pwPath,
			errHas: []string{"web.bind", "afk web password"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			on, logs, err := webStart(tt.web, tt.readErr, cfgPath, tt.path)
			if len(tt.errHas) > 0 {
				if err == nil {
					t.Fatalf("want an error, got on=%v logs=%q", on, logs)
				}
				for _, w := range tt.errHas {
					if !strings.Contains(err.Error(), w) {
						t.Errorf("error %q lacks %q", err, w)
					}
				}
				if on || logs != nil {
					t.Errorf("a refusal gave on=%v logs=%q, want off and no lines", on, logs)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if on != tt.on {
				t.Errorf("on = %v, want %v", on, tt.on)
			}
			if !reflect.DeepEqual(logs, tt.logs) {
				t.Errorf("logs = %q, want %q", logs, tt.logs)
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

	// The note is read from running.json and is about the daemon that wrote
	// it, so it shows only while that daemon is alive, and a later start
	// (which writes the file afresh) takes it away.
	t.Run("a program file the daemon cannot see", func(t *testing.T) {
		r := newDaemonRig(t)
		r.writeConfig(fmt.Sprintf("[[project]]\nname = \"alpha\"\npath = %q\n", r.project()))
		const line = "afk cannot see its own program file, so it will not restart by itself when a new version is installed. Restart it by hand after an install."
		status := func(alive bool) string {
			r.alive = alive
			var so, se bytes.Buffer
			if code := runDaemon([]string{"status"}, &so, &se, r.env()); code != 0 {
				t.Fatalf("exit code = %d, stderr %q", code, se.String())
			}
			return so.String()
		}
		lock := func() {
			unlock, err := daemonstate.Lock(r.stateDir(), 777, daemonTestNow)
			if err != nil {
				t.Fatal(err)
			}
			unlock()
		}
		lock()
		if got := status(true); strings.Contains(got, "cannot see") {
			t.Errorf("the line is shown with binary_unknown false:\n%s", got)
		}
		if err := daemonstate.MarkBinaryUnknown(r.stateDir()); err != nil {
			t.Fatal(err)
		}
		got := status(true)
		if strings.Count(got, line) != 1 || !strings.HasPrefix(got, "The afk daemon is running (pid 777") {
			t.Errorf("want the line once, under the running line, in:\n%s", got)
		}
		if got := status(false); strings.Contains(got, "cannot see") {
			t.Errorf("the line is shown for a daemon that is not running:\n%s", got)
		}
		lock()
		if got := status(true); strings.Contains(got, "cannot see") {
			t.Errorf("the line outlived the next start:\n%s", got)
		}
	})

	// The line comes from usage.json alone: a file with a stored decision
	// gives it, and one written before decisions were stored gives none.
	t.Run("the usage line is read from usage.json", func(t *testing.T) {
		r := newDaemonRig(t)
		env := r.env()
		status := func() string {
			var so, se bytes.Buffer
			if code := runDaemon([]string{"status"}, &so, &se, env); code != 0 {
				t.Fatalf("status exit %d: %s", code, se.String())
			}
			return so.String()
		}
		f := usagestate.File{At: daemonTestNow, Provider: "scripted", Source: usagestate.SourceProbe}
		if err := usagestate.Write(env.state, f); err != nil {
			t.Fatal(err)
		}
		if got := status(); strings.Contains(got, "Usage:") {
			t.Errorf("a usage line with no stored decision:\n%s", got)
		}
		f.Governor = &usagestate.Governor{Action: "hold", Reason: "The last usage reading is 31 minutes old, which is past the 30 minutes limit, so afk is holding new work.", At: daemonTestNow}
		if err := usagestate.Write(env.state, f); err != nil {
			t.Fatal(err)
		}
		if got := status(); !strings.Contains(got, "Usage: not known, so nothing new is started. The last usage reading is 31 minutes old") {
			t.Errorf("no usage line for a stored hold:\n%s", got)
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
// duration there would read as progress); the two "program file" rows (the
// note belongs to a running daemon only, and comes straight after the header
// so it is read before any project); "stopped by the after-merge command" (a
// stopped line must not be worded as a failed deploy when it was not one).
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
			// The usage line is the machine's, so it sits above every
			// project, and a session paused for usage gets no Running line.
			name: "paused for usage",
			in: func() daemonStatus {
				wake := time.Date(2026, 10, 12, 17, 1, 0, 0, time.UTC)
				in := base(daemonProjectStatus{Name: "demo", Root: "/p/demo", HasState: true,
					State: daemonstate.State{At: "2026-10-10T07:15:00Z", Kind: string(daemon.ActUsagePaused), Task: "T1.1", Session: "t1-1",
						Reason: "t1-1 is paused for usage until Mon 12 Oct 17:01 UTC"}})
				in.Usage = &usagestate.Governor{Action: "pause", Reason: "The current session is 91% used, at or past the 90% pause limit, so afk is pausing until it resets.",
					WakeAt: &wake, At: time.Date(2026, 10, 12, 12, 30, 0, 0, time.UTC)}
				return in
			}(),
			want: []string{"started Fri 9 Oct 10:00).\nUsage: paused until Mon 12 Oct 17:01. The current session is 91% used",
				"(as of Mon 12 Oct 12:30)\n\ndemo (/p/demo)", "a session is paused because of the usage limits", "Since:"},
			not: []string{"Running:", "Updated:"},
		},
		{
			name: "no stored decision, no usage line",
			in: base(daemonProjectStatus{Name: "demo", Root: "/p/demo", HasState: true,
				State: daemonstate.State{At: "2026-10-10T07:15:00Z", Kind: string(daemon.ActIdle)}}),
			not: []string{"Usage:"},
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
			name: "program file not seen, daemon running",
			in: func() daemonStatus {
				in := base(daemonProjectStatus{Name: "demo", Root: "/p/demo"})
				in.BinaryUnknown = true
				return in
			}(),
			want: []string{"The afk daemon is running (pid 31, started Fri 9 Oct 10:00).\nafk cannot see its own program file, so it will not restart by itself when a new version is installed. Restart it by hand after an install.\n\ndemo (/p/demo)"},
		},
		{
			name: "program file not seen, daemon not running",
			in: func() daemonStatus {
				in := base(daemonProjectStatus{Name: "demo", Root: "/p/demo"})
				in.BinaryUnknown, in.Running = true, false
				return in
			}(),
			want: []string{"The afk daemon is not running."},
			not:  []string{"cannot see", "program file"},
		},
		{
			name: "stopped by the after-merge command",
			in: base(daemonProjectStatus{Name: "demo", Root: "/p/demo", HasState: true,
				Stopped: true, StopTask: "T1.1", StopReason: "the after-merge command ended with exit code 3",
				State: daemonstate.State{At: "2026-10-11T09:00:00Z", Kind: daemon.KindLineStopped, Task: "T1.1", Session: "t1-1",
					Reason: "the line is stopped; T1.1 is marked done once it is cleared"}}),
			want: []string{`Stopped: the line is stopped after T1.1: the after-merge command ended with exit code 3. No new task will start until you run "afk resume demo", which clears the stop.`,
				"Doing:   the line is stopped, so no new task will start"},
			not: []string{"deploy", "Running:", "program file"},
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
			// Like the owner's task: no session, so no "Running:" line, and
			// the kind is said in words.
			name: "task held for an open question",
			in: base(daemonProjectStatus{Name: "demo", Root: "/p/demo", HasState: true,
				State:  daemonstate.State{At: "2026-10-11T09:00:00Z", Kind: string(daemon.ActQuestionHold), Task: "T1.1", Reason: "waiting on the owner: T1.1 needs 1 open question answered first; see inbox a1b2"},
				Counts: map[ledger.Status]int{ledger.Parked: 1}}),
			want: []string{"Doing:   a task is waiting for you to answer an open question from the plan", "Task:    T1.1\n",
				"Why:     waiting on the owner: T1.1 needs 1 open question answered first; see inbox a1b2", "Since:   Sun 11 Oct 09:00"},
			not: []string{"Running:", "question_hold"},
		},
		{
			name: "open question answered",
			in: base(daemonProjectStatus{Name: "demo", Root: "/p/demo", HasState: true,
				State:  daemonstate.State{At: "2026-10-11T09:00:00Z", Kind: string(daemon.ActQuestionAnswered), Task: "T1.1", Reason: "the open questions T1.1 waited on are answered in the plan; it starts when its turn comes"},
				Counts: map[ledger.Status]int{ledger.Pending: 1}}),
			want: []string{"Doing:   an open question was answered, so the task it held can start", "Task:    T1.1\n"},
			not:  []string{"Running:", "question_answered"},
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

func (p *preflightProvider) Login(context.Context, provider.LoginDeps) (provider.LoginStatus, error) {
	return provider.LoginStatus{}, provider.ErrLoginUnsupported
}

// Name is asked when the real deps are built: the usage source keeps the
// provider's name to tell its own readings from another provider's.
func (p *preflightProvider) Name() string { return "scripted" }

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
			ok, steps := daemonPreflight(p, provider.UnattendedDeps{Home: "/home/x"}, nil, nil)(daemon.Project{Name: "demo", Root: "/p/demo"})
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

// TestDaemonNotifierPush pins what the daemon sends to signed-up devices,
// with a fake send and a temp device list: nothing reaches a network or the
// real state folder.
//
// Load-bearing rows: "non-blocking item" (assumed answers arrive by the
// dozen, so it may not push); "urgent, non-blocking stop item" (the stop item
// is deliberately not blocking yet is the one notice that must reach a phone,
// at high urgency); "gone device" and "send fails" (only gone removes a
// device; a failure is a log line that never repeats the endpoint); and "no
// device" (nothing sent and nothing logged).
func TestDaemonNotifierPush(t *testing.T) {
	proj := daemon.Project{Name: "my app", Root: "/p"}
	blocking := inbox.Item{ID: "a1b2", Task: "T1.1", Kind: "failure", Blocking: true, Title: "It broke", Body: "Details."}
	quiet := inbox.Item{ID: "c3d4", Kind: "notice", Title: "FYI"}
	stop := inbox.Item{ID: "e5f6", Task: "T1.1", Kind: "failure", Title: "The line is stopped after T1.1", Body: "Check the deploy."}
	login := inbox.Item{ID: "g7h8", Kind: "login", Blocking: true, Title: "Sign in again", Body: "The agent is signed out."}
	itemPay := webpush.Payload{Title: "afk: It broke", Body: "Details.\n\nafk inbox show a1b2 (T1.1)", URL: "/p/my%20app/inbox/a1b2", Tag: "afk-item-a1b2"}
	tests := []struct {
		name     string
		devices  []string
		answer   func(webpush.Subscription) (bool, error)
		call     func(n daemonNotifier)
		want     []webpush.Payload
		wantOpts webpush.Options
		logHas   string
		wantLeft int
	}{
		{name: "blocking item", devices: []string{"https://push.example/a"}, wantLeft: 1,
			call: func(n daemonNotifier) { n.item(proj, blocking) }, want: []webpush.Payload{itemPay}},
		{name: "non-blocking item", devices: []string{"https://push.example/a"}, wantLeft: 1,
			call: func(n daemonNotifier) { n.item(proj, quiet) }},
		{name: "urgent, non-blocking stop item", devices: []string{"https://push.example/a"}, wantLeft: 1, wantOpts: webpush.Options{Urgency: "high"},
			call: func(n daemonNotifier) { n.urgent(proj, stop) },
			want: []webpush.Payload{{Title: "afk: The line is stopped after T1.1", Body: "Check the deploy.\n\nafk inbox show e5f6 (T1.1)", URL: "/p/my%20app/inbox/e5f6", Tag: "afk-item-e5f6"}}},
		{name: "urgent, blocking login item", devices: []string{"https://push.example/a"}, wantLeft: 1, wantOpts: webpush.Options{Urgency: "high"},
			call: func(n daemonNotifier) { n.urgent(proj, login) },
			want: []webpush.Payload{{Title: "afk: Sign in again", Body: "The agent is signed out.\n\nafk inbox show g7h8", URL: "/p/my%20app/inbox/g7h8", Tag: "afk-item-g7h8"}}},
		{name: "gone device", devices: []string{"https://push.example/a"}, wantLeft: 0,
			answer: func(webpush.Subscription) (bool, error) { return true, nil },
			call:   func(n daemonNotifier) { n.item(proj, blocking) }, want: []webpush.Payload{itemPay},
			logHas: "is gone and was removed"},
		{name: "send fails", devices: []string{"https://push.example/a"}, wantLeft: 1,
			answer: func(webpush.Subscription) (bool, error) { return false, errors.New("503") },
			call:   func(n daemonNotifier) { n.item(proj, blocking) }, want: []webpush.Payload{itemPay},
			logHas: "failed, device kept"},
		{name: "no device", wantLeft: 0,
			call: func(n daemonNotifier) { n.item(proj, blocking); n.urgent(proj, stop) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newPushRig(t, tt.devices...)
			r.answer = tt.answer
			var log bytes.Buffer
			n := daemonNotifier{
				logf: daemonLogger(&log, func() time.Time { return daemonTestNow }),
				push: pushTarget{dir: r.dir, send: r.send},
			}
			tt.call(n)
			if len(r.pays) != len(tt.want) {
				t.Fatalf("pushed %+v, want %+v", r.pays, tt.want)
			}
			for i, w := range tt.want {
				if r.pays[i] != w {
					t.Errorf("payload %d = %+v, want %+v", i, r.pays[i], w)
				}
				if r.opts[i] != tt.wantOpts {
					t.Errorf("options %d = %+v, want %+v", i, r.opts[i], tt.wantOpts)
				}
			}
			if got := len(webpush.ReadDevices(r.dir)); got != tt.wantLeft {
				t.Errorf("%d devices left, want %d", got, tt.wantLeft)
			}
			if tt.logHas == "" && log.Len() != 0 {
				t.Errorf("logged %q, want silence", log.String())
			}
			if !strings.Contains(log.String(), tt.logHas) {
				t.Errorf("log %q lacks %q", log.String(), tt.logHas)
			}
			if strings.Contains(log.String(), "push.example") {
				t.Errorf("log %q repeats a device endpoint", log.String())
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
	stateHome := t.TempDir()
	st := usagestate.Deps{Getenv: func(k string) string {
		if k == "XDG_STATE_HOME" {
			return stateHome
		}
		return ""
	}}
	d := realDaemonDeps(&preflightProvider{}, "", t.TempDir(), st, daemonconfig.Governor{}, func() []daemon.Project { return nil }, func() time.Time { return daemonTestNow }, &log,
		daemonLogger(&log, func() time.Time { return daemonTestNow }))
	for name, isNil := range map[string]bool{
		"Now": d.Now == nil, "LoadBuild": d.LoadBuild == nil, "Sessions": d.Sessions == nil,
		"Preflight": d.Preflight == nil, "Launch": d.Launch == nil, "RecordTokens": d.RecordTokens == nil,
		"Notify": d.Notify == nil, "Log": d.Log == nil,
		"Ship": d.Ship == nil, "SendPrompt": d.SendPrompt == nil, "Clean": d.Clean == nil,
		"NotifyUrgent": d.NotifyUrgent == nil, "Restart": d.Restart == nil, "Relaunch": d.Relaunch == nil,
		// Without Usage every usage rule is off, and without StopSession a
		// session at its limit is left running: both would be silent.
		"ReadPlan": d.ReadPlan == nil, "PatchPlan": d.PatchPlan == nil,
		"Usage": d.Usage == nil, "Login": d.Login == nil, "StopSession": d.StopSession == nil,
		// Without PlanWaits no planning session ever tells the owner it is
		// waiting (T8.13).
		"PlanWaits": d.PlanWaits == nil,
	} {
		if isNil {
			t.Errorf("realDaemonDeps leaves %s nil", name)
		}
	}
	// Binary is the one field that must be nil here. It is runDaemon's to
	// set, from daemonEnv.binary, and only for a daemon that keeps running:
	// set here it would also reach `afk daemon --once`, and a test's fake
	// loop would never see the seam.
	if d.Binary != nil {
		t.Error("realDaemonDeps sets Binary; runDaemon sets it, and only when --once is not given")
	}
	if daemonSendWait != 2*time.Second {
		t.Errorf("daemonSendWait = %s, want 2s: a pass must not sit on a dead socket", daemonSendWait)
	}
}

// TestRealDaemonEnvBinary checks the real "afk's own program file" read: it
// finds the running program (here the test binary) and answers the same path
// on every call, because the path is resolved once when the env is built.
// Nothing is run and nothing outside the test binary's own file is read.
func TestRealDaemonEnvBinary(t *testing.T) {
	env := realDaemonEnv()
	if env.binary == nil {
		t.Fatal("realDaemonEnv leaves binary nil, so a real daemon would never notice a new version")
	}
	first, err := env.binary()
	if err != nil {
		t.Fatalf("the test binary's own file could not be read: %v", err)
	}
	if first.Path == "" || first.Size <= 0 || first.ModTime.IsZero() {
		t.Errorf("identity = %+v, want a path, a size and a time", first)
	}
	second, err := env.binary()
	if err != nil || second != first {
		t.Errorf("second read = %+v, %v; want the same as the first (%+v)", second, err, first)
	}
	if daemon.NewBinary(first, second, err) {
		t.Error("an untouched program file reads as changed")
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
	// runs are the after-merge commands the fake runner was asked for; the
	// three fields after it are what it answers.
	runs       []afterMergeCall
	amCode     int
	amTimedOut bool
	amErr      error
}

// afterMergeCall is one after-merge command the fake runner was asked for.
type afterMergeCall struct {
	dir, command, logPath string
	timeout               time.Duration
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
		afterMerge: func(dir, command, logPath string, timeout time.Duration) (int, bool, error) {
			r.runs = append(r.runs, afterMergeCall{dir, command, logPath, timeout})
			return r.amCode, r.amTimedOut, r.amErr
		},
		now: func() time.Time { return daemonTestNow },
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
//   - "after-merge": the command, its record and its log all go to the main
//     checkout whether or not the worktree is there, and the record a start
//     mark leaves reads back as "not finished", which is what stops a second
//     run after a kill.
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

	t.Run("after-merge", func(t *testing.T) {
		for _, worktree := range []string{wt, ""} {
			root := t.TempDir()
			req := request(worktree)
			req.Project.Root = root
			r := &shipRig{amCode: 3}
			d := shipDeps(req, r.wiring())
			logPath := filepath.Join(root, ".afk", "build", "after-merge.log")

			if _, _, _, ok := d.AfterMergeRecord(); ok {
				t.Errorf("worktree %q: a record was read in a project that has none", worktree)
			}
			if got := d.AfterMergeTail(); got != "" {
				t.Errorf("worktree %q: tail with no log = %q, want empty", worktree, got)
			}

			// The start mark: on disk in the main checkout, and not finished.
			started := daemonTestNow.Add(-time.Minute)
			if err := d.MarkAfterMerge("T1.1", "abc", ship.AfterMergeRun{}, started); err != nil {
				t.Fatal(err)
			}
			rec, ok := daemonstate.ReadAfterMerge(root)
			if want := (daemonstate.AfterMerge{Task: "T1.1", MergedSHA: "abc", StartedAt: "2026-10-11T09:29:00Z"}); !ok || rec != want {
				t.Errorf("worktree %q: start mark on disk = %+v, %v; want %+v", worktree, rec, ok, want)
			}
			task, sha, run, ok := d.AfterMergeRecord()
			if !ok || task != "T1.1" || sha != "abc" || run != (ship.AfterMergeRun{}) {
				t.Errorf("worktree %q: start mark read back as %q, %q, %+v, %v; want T1.1, abc and a run that has not finished", worktree, task, sha, run, ok)
			}

			code, timedOut, err := d.RunAfterMerge("make install", 10*time.Minute)
			if code != 3 || timedOut || err != nil {
				t.Errorf("worktree %q: the runner's answer came back as %d, %v, %v", worktree, code, timedOut, err)
			}
			if want := []afterMergeCall{{root, "make install", logPath, 10 * time.Minute}}; !reflect.DeepEqual(r.runs, want) {
				t.Errorf("worktree %q: ran %+v, want %+v (the main checkout, never the worktree)", worktree, r.runs, want)
			}

			// The finish mark keeps the start time and adds how it ended.
			done := ship.AfterMergeRun{Finished: true, ExitCode: 3, FinishedAt: daemonTestNow}
			if err := d.MarkAfterMerge("T1.1", "abc", done, started); err != nil {
				t.Fatal(err)
			}
			rec, _ = daemonstate.ReadAfterMerge(root)
			if want := (daemonstate.AfterMerge{Task: "T1.1", MergedSHA: "abc", StartedAt: "2026-10-11T09:29:00Z", FinishedAt: "2026-10-11T09:30:00Z", ExitCode: 3}); rec != want {
				t.Errorf("worktree %q: finish mark on disk = %+v, want %+v", worktree, rec, want)
			}
			if _, _, run, ok := d.AfterMergeRecord(); !ok || run != done {
				t.Errorf("worktree %q: finish mark read back as %+v, %v; want %+v", worktree, run, ok, done)
			}

			// The tail is the end of the log in the main checkout.
			var lines []string
			for i := 1; i <= 30; i++ {
				lines = append(lines, fmt.Sprintf("line %d", i))
			}
			if err := os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if got, want := d.AfterMergeTail(), strings.Join(lines[10:], "\n"); got != want {
				t.Errorf("worktree %q: tail = %q, want the last 20 lines", worktree, got)
			}
			if worktree != "" {
				if _, err := os.Stat(filepath.Join(worktree, ".afk")); err == nil {
					t.Error("something was written under the worktree")
				}
			}
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

// TestDaemonAfterMergeRecord pins both directions between the run record on
// disk and what ship reads, and that the two packages agree on where the log
// is.
//
// Load-bearing rows: "finish time does not parse" and "finish time empty but
// an exit code" must both read as not finished. The first is a damaged
// record, and believing its exit code 0 would carry the ship on to the deploy
// check on the word of a broken file; not finished stops the line instead.
// "start mark drops a result" keeps a start mark from ever looking finished.
func TestDaemonAfterMergeRecord(t *testing.T) {
	finished := time.Date(2026, 10, 11, 9, 40, 0, 0, time.UTC)
	t.Run("read", func(t *testing.T) {
		tests := []struct {
			name string
			rec  daemonstate.AfterMerge
			want ship.AfterMergeRun
		}{
			{"start mark", daemonstate.AfterMerge{Task: "T1.1", MergedSHA: "abc", StartedAt: "2026-10-11T09:30:00Z"}, ship.AfterMergeRun{}},
			{"finished well", daemonstate.AfterMerge{StartedAt: "2026-10-11T09:30:00Z", FinishedAt: "2026-10-11T09:40:00Z"},
				ship.AfterMergeRun{Finished: true, FinishedAt: finished}},
			{"finished with an exit code", daemonstate.AfterMerge{FinishedAt: "2026-10-11T09:40:00Z", ExitCode: 3},
				ship.AfterMergeRun{Finished: true, ExitCode: 3, FinishedAt: finished}},
			{"timed out", daemonstate.AfterMerge{FinishedAt: "2026-10-11T09:40:00Z", ExitCode: -1, TimedOut: true},
				ship.AfterMergeRun{Finished: true, ExitCode: -1, TimedOut: true, FinishedAt: finished}},
			{"finish time in another zone", daemonstate.AfterMerge{FinishedAt: "2026-10-11T11:40:00+02:00"},
				ship.AfterMergeRun{Finished: true, FinishedAt: finished}},
			{"finish time does not parse", daemonstate.AfterMerge{StartedAt: "2026-10-11T09:30:00Z", FinishedAt: "soon"}, ship.AfterMergeRun{}},
			{"finish time empty but an exit code", daemonstate.AfterMerge{ExitCode: 3, TimedOut: true}, ship.AfterMergeRun{}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := afterMergeRun(tt.rec)
				if got.Finished != tt.want.Finished || got.ExitCode != tt.want.ExitCode || got.TimedOut != tt.want.TimedOut ||
					got.Found || !got.FinishedAt.Equal(tt.want.FinishedAt) {
					t.Errorf("got %+v, want %+v", got, tt.want)
				}
			})
		}
	})
	t.Run("write", func(t *testing.T) {
		started := time.Date(2026, 10, 11, 11, 30, 0, 0, time.FixedZone("x", 2*60*60))
		tests := []struct {
			name string
			run  ship.AfterMergeRun
			want daemonstate.AfterMerge
		}{
			{"start mark", ship.AfterMergeRun{},
				daemonstate.AfterMerge{Task: "T1.1", MergedSHA: "abc", StartedAt: "2026-10-11T09:30:00Z"}},
			{"start mark drops a result", ship.AfterMergeRun{ExitCode: 3, TimedOut: true, FinishedAt: finished},
				daemonstate.AfterMerge{Task: "T1.1", MergedSHA: "abc", StartedAt: "2026-10-11T09:30:00Z"}},
			{"finished", ship.AfterMergeRun{Finished: true, ExitCode: 2, FinishedAt: finished},
				daemonstate.AfterMerge{Task: "T1.1", MergedSHA: "abc", StartedAt: "2026-10-11T09:30:00Z", FinishedAt: "2026-10-11T09:40:00Z", ExitCode: 2}},
			{"timed out", ship.AfterMergeRun{Finished: true, ExitCode: -1, TimedOut: true, FinishedAt: finished},
				daemonstate.AfterMerge{Task: "T1.1", MergedSHA: "abc", StartedAt: "2026-10-11T09:30:00Z", FinishedAt: "2026-10-11T09:40:00Z", ExitCode: -1, TimedOut: true}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := afterMergeRecord("T1.1", "abc", tt.run, started)
				if got != tt.want {
					t.Errorf("got %+v, want %+v", got, tt.want)
				}
				// What was written reads back as what was meant.
				back := afterMergeRun(got)
				if back.Finished != tt.run.Finished {
					t.Errorf("read back Finished = %v, want %v", back.Finished, tt.run.Finished)
				}
			})
		}
	})
	// ship cannot import daemonstate, so the log's path is written in both.
	// The stop item shows ship's; the runner writes daemonstate's.
	t.Run("log path", func(t *testing.T) {
		root := t.TempDir()
		if got, want := daemonstate.AfterMergeLogPath(root), filepath.Join(root, filepath.FromSlash(ship.AfterMergeLog)); got != want {
			t.Errorf("daemonstate writes the log to %q, ship tells the owner to look in %q", got, want)
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
//   - the after-merge rows go through the real wiring to the real record
//     file. "runs in the project root" and "worktree gone" pin the one folder
//     the command may run in (PLAN T2.2a: the main checkout); "left
//     unfinished" is the crash case, where the record alone must stop the
//     line with no second run; "another merge's record" must not be taken
//     for this ship's, or the command would be skipped.
func TestDaemonShip(t *testing.T) {
	entry := ledger.Entry{ID: "T1.1", Status: ledger.Shipping, Session: "t1-1", Branch: "t1-1"}
	afterMerge := ship.Progress{State: ship.AfterMerge, PR: 7, MergedSHA: "abc"}
	tests := []struct {
		name     string
		cfgErr   error
		progress ship.Progress
		// command is the project's after_merge; worktree says whether the
		// session is still on disk; record is the run record already there.
		command  string
		worktree bool
		record   *daemonstate.AfterMerge
		amCode   int
		wantKind ship.OutcomeKind
		wantHas  string
		// wantState is the state the ship comes back in, when it moves.
		wantState ship.State
		wantRuns  int
		// wantRecord is the record expected on disk afterwards, nil for none.
		wantRecord *daemonstate.AfterMerge
	}{
		{name: "config unreadable", cfgErr: errors.New("afk.toml is not valid TOML"), progress: ship.Progress{State: ship.PROpen, PR: 7},
			wantKind: ship.OutcomeWait, wantHas: "afk.toml is not valid TOML"},
		{name: "no session and no base", progress: ship.Progress{}, wantKind: ship.OutcomePark, wantHas: "base"},
		{name: "last state", progress: ship.Progress{State: ship.DeployHealthy, PR: 7, MergedSHA: "abc"}, wantKind: ship.OutcomeDone},
		{name: "after-merge command runs in the project root", progress: afterMerge, command: "make install", worktree: true,
			wantKind: ship.OutcomeWait, wantHas: "the after-merge command finished", wantRuns: 1,
			wantRecord: &daemonstate.AfterMerge{Task: "T1.1", MergedSHA: "abc", StartedAt: "2026-10-11T09:30:00Z", FinishedAt: "2026-10-11T09:30:00Z"}},
		{name: "after-merge command, worktree gone", progress: afterMerge, command: "make install",
			wantKind: ship.OutcomeWait, wantHas: "the after-merge command finished", wantRuns: 1,
			wantRecord: &daemonstate.AfterMerge{Task: "T1.1", MergedSHA: "abc", StartedAt: "2026-10-11T09:30:00Z", FinishedAt: "2026-10-11T09:30:00Z"}},
		{name: "after-merge command fails", progress: afterMerge, command: "make install", worktree: true, amCode: 3,
			wantKind: ship.OutcomeWait, wantHas: "exit code 3", wantRuns: 1,
			wantRecord: &daemonstate.AfterMerge{Task: "T1.1", MergedSHA: "abc", StartedAt: "2026-10-11T09:30:00Z", FinishedAt: "2026-10-11T09:30:00Z", ExitCode: 3}},
		{name: "after-merge record left unfinished", progress: afterMerge, command: "make install", worktree: true,
			record:   &daemonstate.AfterMerge{Task: "T1.1", MergedSHA: "abc", StartedAt: "2026-10-11T09:00:00Z"},
			wantKind: ship.OutcomeStop, wantHas: "afk was stopped while the after-merge command ran", wantState: ship.Stopped, wantRuns: 0,
			wantRecord: &daemonstate.AfterMerge{Task: "T1.1", MergedSHA: "abc", StartedAt: "2026-10-11T09:00:00Z"}},
		{name: "after-merge record with a finish time that does not parse", progress: afterMerge, command: "make install",
			record:   &daemonstate.AfterMerge{Task: "T1.1", MergedSHA: "abc", StartedAt: "2026-10-11T09:00:00Z", FinishedAt: "soon"},
			wantKind: ship.OutcomeStop, wantHas: "afk was stopped while the after-merge command ran", wantState: ship.Stopped, wantRuns: 0,
			wantRecord: &daemonstate.AfterMerge{Task: "T1.1", MergedSHA: "abc", StartedAt: "2026-10-11T09:00:00Z", FinishedAt: "soon"}},
		{name: "another merge's record is not this ship's", progress: afterMerge, command: "make install", worktree: true,
			record:   &daemonstate.AfterMerge{Task: "T1.1", MergedSHA: "older", StartedAt: "2026-10-10T09:00:00Z", FinishedAt: "2026-10-10T09:01:00Z"},
			wantKind: ship.OutcomeWait, wantHas: "the after-merge command finished", wantRuns: 1,
			wantRecord: &daemonstate.AfterMerge{Task: "T1.1", MergedSHA: "abc", StartedAt: "2026-10-11T09:30:00Z", FinishedAt: "2026-10-11T09:30:00Z"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &shipRig{amCode: tt.amCode}
			root := t.TempDir()
			worktree := ""
			if tt.worktree {
				worktree = t.TempDir()
			}
			if tt.record != nil {
				if err := daemonstate.WriteAfterMerge(root, *tt.record); err != nil {
					t.Fatal(err)
				}
			}
			fn := daemonShip(func(got string) (projectconfig.Config, error) {
				if got != root {
					t.Errorf("config loaded from %q, want the project root", got)
				}
				return projectconfig.Config{Ship: projectconfig.Ship{MergeMethod: "merge", Deploys: true, DeployTimeout: 15 * time.Minute,
					AfterMerge: tt.command, AfterMergeTimeout: 7 * time.Minute}}, tt.cfgErr
			}, r.wiring())
			out := fn(daemon.ShipRequest{
				Project: daemon.Project{Name: "demo", Root: root}, Entry: entry, Progress: tt.progress, Worktree: worktree,
				Save: func(ship.Progress) error { return nil },
				Stop: func(string, string) error { return nil },
			})
			t.Logf("outcome: %s: %s", out.Kind, out.Reason)
			if out.Kind != tt.wantKind {
				t.Fatalf("outcome = %+v, want kind %q", out, tt.wantKind)
			}
			if !strings.Contains(out.Reason, tt.wantHas) {
				t.Errorf("reason %q lacks %q", out.Reason, tt.wantHas)
			}
			want := tt.progress
			if tt.wantState != "" {
				want.State = tt.wantState
			}
			if out.Progress.State != want.State || out.Progress.PR != want.PR || out.Progress.MergedSHA != want.MergedSHA {
				t.Errorf("progress = %+v, want state %q with the rest unchanged (%+v)", out.Progress, want.State, tt.progress)
			}
			if tt.wantState == "" && out.Progress != tt.progress {
				t.Errorf("progress = %+v, want it unchanged (%+v)", out.Progress, tt.progress)
			}
			if len(r.calls) != 0 || len(r.typed) != 0 {
				t.Errorf("ran %+v and typed %q, want nothing", r.calls, r.typed)
			}
			if len(r.runs) != tt.wantRuns {
				t.Fatalf("after-merge runs = %+v, want %d", r.runs, tt.wantRuns)
			}
			for _, run := range r.runs {
				if want := (afterMergeCall{root, tt.command, filepath.Join(root, ".afk", "build", "after-merge.log"), 7 * time.Minute}); run != want {
					t.Errorf("ran %+v, want %+v: the project root is the only folder the command may run in", run, want)
				}
			}
			rec, ok := daemonstate.ReadAfterMerge(root)
			switch {
			case tt.wantRecord == nil && ok:
				t.Errorf("a run record was written: %+v", rec)
			case tt.wantRecord != nil && (!ok || rec != *tt.wantRecord):
				t.Errorf("record under the project root = %+v, %v; want %+v", rec, ok, *tt.wantRecord)
			}
			if worktree != "" {
				if entries, err := os.ReadDir(worktree); err != nil || len(entries) != 0 {
					t.Errorf("the worktree holds %v (err %v), want nothing written there", entries, err)
				}
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
		string(daemon.ActRestart): true, string(daemon.ActRelaunch): true, string(daemon.ActContinue): true,
		// A woken session is at work again. The wake's give-up is parked,
		// like the recovery one, and is false by not being here.
		string(daemon.ActWake): true,
		// The only usage kind that is true: the paused session is being
		// started again. The other five name a session whose agent is stopped
		// or held back, and are false by not being here.
		string(daemon.ActUsageResume): true,
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

// TestDaemonStateChurns pins which states show "Updated" instead of "Since".
// Load-bearing: only a ship step churns. A usage-paused session must show
// "Since", because how long it has been paused is what the owner wants to
// read, and its state file is rewritten only when something changes.
func TestDaemonStateChurns(t *testing.T) {
	for _, kind := range daemon.Kinds() {
		t.Run(kind, func(t *testing.T) {
			want := kind == string(daemon.ActShipStep)
			if got := daemonStateChurns(daemonstate.State{Kind: kind, Task: "T1.1", Session: "t1-1"}); got != want {
				t.Errorf("got %v, want %v", got, want)
			}
		})
	}
}

// TestDaemonSettings checks that the machine config's recovery keys reach the
// loop. Load-bearing: the max_restarts = 0 row, because zero is a real setting
// (never restart) and must not be replaced by a default on the way; and the
// two max_wakes rows for the same reason, with "wakes reach the loop" also
// showing that the key is not mixed up with max_restarts beside it.
func TestDaemonSettings(t *testing.T) {
	tests := []struct {
		name string
		cfg  daemonconfig.Config
		want daemon.Settings
	}{
		{"recovery keys reach the loop",
			daemonconfig.Config{StallAfter: 5 * time.Minute, MaxRestarts: 1, RestartBackoff: 30 * time.Second},
			daemon.Settings{StallAfter: 5 * time.Minute, MaxRestarts: 1, RestartBackoff: 30 * time.Second}},
		{"zero restarts stays zero",
			daemonconfig.Config{StallAfter: time.Minute, MaxRestarts: 0, RestartBackoff: time.Minute},
			daemon.Settings{StallAfter: time.Minute, MaxRestarts: 0, RestartBackoff: time.Minute}},
		{"wakes reach the loop",
			daemonconfig.Config{StallAfter: time.Minute, MaxRestarts: 3, RestartBackoff: time.Minute, MaxWakes: 2},
			daemon.Settings{StallAfter: time.Minute, MaxRestarts: 3, RestartBackoff: time.Minute, MaxWakes: 2}},
		{"zero wakes stays zero",
			daemonconfig.Config{StallAfter: time.Minute, MaxRestarts: 3, RestartBackoff: time.Minute, MaxWakes: 0},
			daemon.Settings{StallAfter: time.Minute, MaxRestarts: 3, RestartBackoff: time.Minute, MaxWakes: 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := daemonSettings(tt.cfg); got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

// nebariPlan reads the nebari fixture. nebariT94Question is the text of the first open row of it
// whose "Needed by" names T9.4.
func nebariPlan(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("internal", "masterplan", "testdata", "nebari-plan.md"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func nebariT94Question(t *testing.T, plan []byte) string {
	t.Helper()
	for _, q := range masterplan.Questions(plan) {
		if q.Open && len(q.NeededBy) > 0 && q.NeededBy[0] == "T9.4" {
			return q.Text
		}
	}
	t.Fatal("the fixture has no open row naming T9.4")
	return ""
}

// answerEdit is the Edit the loop hands PatchPlan, built the way the loop
// builds it (daemon.go patchAnswer): masterplan.AnswerRow, and ErrRowAnswered
// when the row is not open exactly once.
func answerEdit(question, answer string) func([]byte) ([]byte, []byte, error) {
	return func(plan []byte) ([]byte, []byte, error) {
		old, new, ok := masterplan.AnswerRow(plan, question, "2026-10-13", answer)
		if !ok {
			return nil, nil, daemon.ErrRowAnswered
		}
		return old, new, nil
	}
}

// prGit is patchGit's counterpart for pull-request mode: a repository on
// main with an origin, no branch planpatch.PRBranch yet and no open pull request. A
// "git worktree add" copies the plan into the new folder, since the fake
// makes no checkout, so the change is made there and not in the main
// checkout.
type prGit struct {
	root, rel string
	calls     []string
}

func (g *prGit) run(dir, name string, args ...string) (string, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	g.calls = append(g.calls, line)
	exit := func(code int) (string, error) {
		return "", &ship.CmdError{Cmd: line, Code: code, Err: fmt.Errorf("exit status %d", code)}
	}
	switch {
	case strings.HasPrefix(line, "git rev-parse --is-inside-work-tree"):
		return "true\n", nil
	case strings.HasPrefix(line, "git status --porcelain"):
		return "", nil
	case line == "git remote get-url origin":
		return "git@example.test:me/app.git\n", nil
	case line == "git branch --show-current":
		return "main\n", nil
	case strings.HasPrefix(line, "git rev-parse --verify"):
		return exit(1)
	case strings.HasPrefix(line, "git worktree add"):
		wt := args[len(args)-2]
		data, err := os.ReadFile(filepath.Join(g.root, g.rel))
		if err != nil {
			return exit(1)
		}
		if err := os.MkdirAll(filepath.Join(wt, filepath.Dir(g.rel)), 0o755); err != nil {
			return exit(1)
		}
		if err := os.WriteFile(filepath.Join(wt, g.rel), data, 0o644); err != nil {
			return exit(1)
		}
		return "", nil
	case strings.HasPrefix(line, "git add"), strings.HasPrefix(line, "git commit"),
		strings.HasPrefix(line, "git push"), strings.HasPrefix(line, "git worktree remove"):
		return "", nil
	case strings.HasPrefix(line, "gh pr list"):
		return "[]\n", nil
	case strings.HasPrefix(line, "gh pr create"):
		return "https://example.test/me/app/pull/7\n", nil
	}
	return exit(1)
}

// TestPatchPlan drives the loop's PatchPlan hook against a temp project that
// holds the nebari plan, with git and gh scripted.
//
// Load-bearing rows:
//   - "answers the row": the answer is in the plan file, reads as answered
//     with the pipe escaped, and the commit names the plan alone.
//   - "already answered": errors.Is(err, daemon.ErrRowAnswered) must hold
//     through planpatch.Run, or the loop would take a refusal for a failure.
//   - "via_pr": the pull request address comes back and the main checkout's
//     plan is byte for byte as it was.
//   - "config does not load": an error and no git command at all, so a
//     project that asked for pull requests never gets a commit on its base.
func TestPatchPlan(t *testing.T) {
	const planRel = "docs/PLAN.md"
	base := nebariPlan(t)
	question := nebariT94Question(t, base)
	old, new, ok := masterplan.AnswerRow(base, question, "2026-10-12", "British")
	if !ok {
		t.Fatal("the fixture row cannot be answered")
	}
	answered := bytes.Replace(base, old, new, 1)

	tests := []struct {
		name      string
		toml      string
		plan      []byte
		answer    string
		viaPR     bool
		wantErr   func(error) bool
		wantPR    string
		wantRow   string // text the plan must hold afterwards, "" for none
		unchanged bool
		wantGit   []string // prefixes of git commands that change something
	}{
		{
			name:    "answers the row",
			toml:    "plan = \"docs/PLAN.md\"\n",
			plan:    base,
			answer:  "Use Frankfurt | eu-central",
			wantRow: "**Answered 2026-10-13:** Use Frankfurt \\| eu-central",
			wantGit: []string{"git add -- docs/PLAN.md", "git commit -m docs(plan): Answer an open question for T9.4 -- docs/PLAN.md"},
		},
		{
			name:    "already answered",
			toml:    "plan = \"docs/PLAN.md\"\n",
			plan:    answered,
			answer:  "later",
			wantErr: func(err error) bool { return errors.Is(err, daemon.ErrRowAnswered) },
			// The plan holds the earlier answer and the patch must not touch it.
			unchanged: true,
		},
		{
			name:      "via_pr",
			toml:      "plan = \"docs/PLAN.md\"\n[planning]\nvia_pr = true\n",
			plan:      base,
			answer:    "British",
			viaPR:     true,
			wantPR:    "https://example.test/me/app/pull/7",
			unchanged: true,
			wantGit:   []string{"git add -- docs/PLAN.md", "git commit -m docs(plan): Answer an open question for T9.4 -- docs/PLAN.md", "git push -u origin " + planpatch.PRBranch},
		},
		{
			name:      "config does not load",
			toml:      "plan = [\n",
			plan:      base,
			answer:    "British",
			wantErr:   func(err error) bool { return err != nil && !errors.Is(err, daemon.ErrRowAnswered) },
			unchanged: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writePlanFile(t, root, planRel, string(tt.plan))
			writePlanFile(t, root, "afk.toml", tt.toml)
			before := append([]byte(nil), tt.plan...)

			var run ship.RunFunc
			var calls func() []string
			if tt.viaPR {
				g := &prGit{root: root, rel: planRel}
				run, calls = g.run, func() []string { return g.calls }
			} else {
				g := &patchGit{repo: true}
				run = g.run
				calls = func() []string {
					var out []string
					for _, c := range g.calls {
						out = append(out, strings.Join(c, " "))
					}
					return out
				}
			}
			tempDir := func() (string, error) { return t.TempDir(), nil }
			hook := daemonPatchPlan(projectconfig.Load, run, tempDir)

			res, err := hook(daemon.Project{Name: "app", Root: root}, daemon.PlanPatchRequest{
				PlanPath: filepath.Join(root, planRel),
				Edit:     answerEdit(question, tt.answer),
				Message:  "docs(plan): Answer an open question for T9.4",
			})
			switch {
			case tt.wantErr != nil:
				if err == nil || !tt.wantErr(err) {
					t.Fatalf("error = %v, not the one wanted", err)
				}
			case err != nil:
				t.Fatalf("PatchPlan: %v", err)
			}
			if res.PR != tt.wantPR {
				t.Errorf("PR = %q, want %q", res.PR, tt.wantPR)
			}
			if tt.viaPR && len(res.Notes) == 0 {
				t.Error("a pull request came back with no note for the owner")
			}
			got, rerr := os.ReadFile(filepath.Join(root, planRel))
			if rerr != nil {
				t.Fatal(rerr)
			}
			if tt.unchanged && !bytes.Equal(got, before) {
				t.Error("the main checkout's plan was changed")
			}
			if tt.wantRow != "" {
				if !strings.Contains(string(got), tt.wantRow) {
					t.Errorf("the plan lacks %q", tt.wantRow)
				}
				if open := masterplan.Questions(got); len(open) == 0 {
					t.Fatal("no rows read back")
				}
				for _, q := range masterplan.Questions(got) {
					if q.Text == question && q.Open {
						t.Error("the row still reads as open")
					}
				}
			}
			var writes []string
			for _, c := range calls() {
				for _, w := range []string{"git add", "git commit", "git push"} {
					if strings.HasPrefix(c, w) {
						writes = append(writes, c)
					}
				}
			}
			if !reflect.DeepEqual(writes, tt.wantGit) {
				t.Errorf("git writes:\n  got  %q\n  want %q", writes, tt.wantGit)
			}
		})
	}
}

// TestPatchPlanNebariRowPassesTheGuard builds the edit the loop builds for
// the first open row naming T9.4 in the nebari fixture and shows
// planpatch.Apply accepts it, and that the checker finds no error the plan
// did not already have. Load-bearing: Apply's lint step is what stands
// between an owner's free-text answer and a plan that no longer parses.
func TestPatchPlanNebariRowPassesTheGuard(t *testing.T) {
	plan := nebariPlan(t)
	question := nebariT94Question(t, plan)
	old, new, err := answerEdit(question, "British, from the browser | then the setting")(plan)
	if err != nil {
		t.Fatal(err)
	}
	res, err := planpatch.Apply(planpatch.Request{Plan: plan, Old: old, New: new})
	if err != nil {
		t.Fatalf("Apply refused the answer: %v", err)
	}
	before, after := planlint.Lint(plan).Summary, planlint.Lint(res.Plan).Summary
	if after.Errors > before.Errors {
		t.Errorf("errors went from %d to %d", before.Errors, after.Errors)
	}
	if after.OpenQuestions != before.OpenQuestions-1 {
		t.Errorf("open questions went from %d to %d, want one fewer", before.OpenQuestions, after.OpenQuestions)
	}
}

// TestDaemonStartCheck covers backend (afk, rex, carried error, unreadable
// config) x Rex facts (not installed, no answer, answers) at the daemon's
// launch gate. Load-bearing rows:
//   - every afk row passes with zero Rex calls, so the build loop of a machine
//     that never chose Rex is untouched.
//   - the refusal rows go through daemonPreflight, which is what raises the
//     one blocking failure item and launches nothing; the steps carry the
//     plain Rex message.
//   - "config unreadable" refuses rather than starting on afk's host.
func TestDaemonStartCheck(t *testing.T) {
	carried := errors.New("config.toml: [runtime] backend must be \"afk\" or \"rex\"")
	missing := func(string) (string, error) { return "", errors.New("not found") }
	found := func(string) (string, error) { return "/usr/bin/rex", nil }
	rows := []struct {
		name        string
		cfg         daemonconfig.Config
		loadErr     error
		look        func(string) (string, error)
		statusErr   error
		wantMsg     string // "" passes
		wantMethods int
	}{
		{name: "afk not installed", cfg: daemonconfig.Config{Runtime: daemonconfig.RuntimeConfig{Backend: "afk"}}, look: missing},
		{name: "afk no answer", cfg: daemonconfig.Config{Runtime: daemonconfig.RuntimeConfig{Backend: "afk"}}, look: found, statusErr: errors.New("down")},
		{name: "afk answers", cfg: daemonconfig.Config{Runtime: daemonconfig.RuntimeConfig{Backend: "afk"}}, look: found},
		{name: "rex not installed", cfg: daemonconfig.Config{Runtime: daemonconfig.RuntimeConfig{Backend: "rex"}}, look: missing, wantMsg: "Rex is not installed"},
		{name: "rex no answer", cfg: daemonconfig.Config{Runtime: daemonconfig.RuntimeConfig{Backend: "rex"}}, look: found, statusErr: errors.New("down"),
			wantMsg: "does not answer", wantMethods: 1},
		{name: "rex answers", cfg: daemonconfig.Config{Runtime: daemonconfig.RuntimeConfig{Backend: "rex"}}, look: found, wantMethods: 1},
		{name: "carried error", cfg: daemonconfig.Config{RuntimeErr: carried}, look: found, wantMsg: "[runtime] backend"},
		{name: "config unreadable", loadErr: errors.New("bad toml"), look: found, wantMsg: "could not read config.toml"},
	}
	for _, tt := range rows {
		t.Run(tt.name, func(t *testing.T) {
			fake := (&rex.Fake{}).On("server.status", `{"api_revision":7}`, tt.statusErr)
			sd := session.Deps{Rex: session.RexDeps{Caller: fake, LookPath: tt.look}}
			check := daemonStartCheck(sd, func() (daemonconfig.Config, error) { return tt.cfg, tt.loadErr })
			p := &preflightProvider{modeOK: true, pluginOK: true}
			ok, steps := daemonPreflight(p, provider.UnattendedDeps{Home: "/home/x"}, nil, check)(daemon.Project{Name: "demo", Root: "/p/demo"})
			if ok != (tt.wantMsg == "") {
				t.Errorf("ok = %v, steps %q; want pass = %v", ok, steps, tt.wantMsg == "")
			}
			if tt.wantMsg != "" && !strings.Contains(steps, tt.wantMsg) {
				t.Errorf("steps %q lack %q", steps, tt.wantMsg)
			}
			if got := len(fake.Calls()); got != tt.wantMethods {
				t.Errorf("rex calls = %d, want %d", got, tt.wantMethods)
			}
		})
	}
}
