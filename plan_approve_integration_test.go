package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/daemon"
	"github.com/dphaener/afk/internal/daemonconfig"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/plansession"
	"github.com/dphaener/afk/internal/profile"
)

// TestApproveThenDaemonStartsTheFirstTask is the acceptance test for "approve
// and go": a daemon that is already running, with no projects, starts the
// first task of a plan the moment the owner approves it.
//
// The two commands are the real ones, runDaemon and runPlanApprove, and they
// meet only where the two processes would: the machine's config.toml and the
// project's files, both in temp folders. Under them are the daemon rig's fake
// loop deps (Launch records and starts nothing) and a scripted git, so no
// session, no agent CLI and no real git is involved.
//
// What makes it a test of "no restart":
//   - runDaemon is called once, on a goroutine of its own, and is still inside
//     that one call when the launch is seen. A daemon that read its project
//     list only at start-up never launches, and the test fails on its wait.
//   - Approve is not run until the daemon has finished reading the config for
//     its first pass and found it empty (the rig's look at the program file
//     comes straight after that read). Run earlier, approve could win the
//     race to the config and a start-up-only read would pass too.
//   - The config holds no project before approve, and the test checks that.
//
// Every wait is on a channel with a time limit; the limits only bound a
// failure. A passing run takes about one tick (1s, the shortest the config
// allows), which is the wait for the pass after the approve.
func TestApproveThenDaemonStartsTheFirstTask(t *testing.T) {
	const limit = 30 * time.Second

	r := newDaemonRig(t)
	r.writeConfig("# my afk settings\n[daemon]\ntick = \"1s\"\n")
	r.launched = make(chan daemon.LaunchRequest, 8)
	ctx, cancel := context.WithTimeout(context.Background(), 2*limit)
	defer cancel()
	r.ctx = ctx
	firstPass := make(chan struct{})
	r.binary = func(read int) (daemon.Binary, error) {
		if read == 2 { // 1 is the start; 2 is the first pass, after its config read
			close(firstPass)
		}
		return daemonTestBinary, nil
	}

	// The fixture project: a folder git knows, a plan whose first task waits
	// on nothing and on no open question, and a new planning session that has
	// reached review. It has no afk.toml and no ledger; approve makes both.
	root := filepath.Join(t.TempDir(), "recipes")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	seedPlanSession(t, root, plansession.PhaseReview, "no: a command line", daemonTestPlan)
	writePlanFile(t, root, ".gitignore", ".afk/\nafk.local.toml\n")

	env := r.env()
	var daemonOut, daemonErr bytes.Buffer
	exited := make(chan int, 1)
	go func() { exited <- runDaemon(nil, &daemonOut, &daemonErr, env) }()
	// The daemon's log is read only after its goroutine has ended.
	stopDaemon := func() (code int, log string) {
		cancel()
		select {
		case code = <-exited:
		case <-time.After(limit):
			t.Fatal("the daemon did not stop when asked")
		}
		return code, daemonErr.String()
	}

	select {
	case <-firstPass:
	case code := <-exited:
		t.Fatalf("the daemon exited %d with no projects configured; it must start and wait:\n%s", code, daemonErr.String())
	case <-time.After(limit):
		_, log := stopDaemon()
		t.Fatalf("the daemon never began a pass:\n%s", log)
	}
	if cfg, err := daemonconfig.Load(env.config); err != nil || len(cfg.Projects) != 0 {
		t.Fatalf("before approve the config lists %d project(s) (err %v), want none", len(cfg.Projects), err)
	}
	select {
	case req := <-r.launched:
		t.Fatalf("the daemon launched %+v before any plan was approved", req)
	default:
	}

	// `afk plan approve`, run in the project folder, against the same config
	// and state folders the daemon was started with.
	git := &approveGit{dirty: true, origin: true}
	plan := &planFake{commonDir: filepath.Join(root, ".git")}
	approve := approveDeps{
		plan:   plan.deps(root),
		run:    git.run,
		exists: fileExists,
		create: createExclusive,
		project: projectEnv{
			Deps: env.config,
			// The real writer: a temp file and a rename, so the daemon reading
			// on its own goroutine sees the old file or the new one and never
			// half of either.
			write:    profile.Write,
			mkdirAll: os.MkdirAll,
			cwd:      func() (string, error) { return root, nil },
			gitCommonDir: func(dir string) string {
				if dir == root {
					return filepath.Join(root, ".git")
				}
				return ""
			},
			isDir: env.isDir,
		},
		stateDir:      func() (string, error) { return r.stateDir(), nil },
		ledgerExists:  ledger.Exists,
		ledgerInit:    ledger.Init,
		writeState:    plansession.WriteState,
		daemonRunning: func(string) bool { return true },
	}
	var approveOut, approveErr bytes.Buffer
	if code := runPlanApprove(nil, &approveOut, &approveErr, approve); code != 0 {
		_, log := stopDaemon()
		t.Fatalf("afk plan approve exited %d\nstdout: %s\nstderr: %s\ndaemon log:\n%s", code, approveOut.String(), approveErr.String(), log)
	}
	t.Logf("afk plan approve:\n%s", approveOut.String())

	var got daemon.LaunchRequest
	select {
	case got = <-r.launched:
	case code := <-exited:
		t.Fatalf("the daemon exited %d after the approve instead of starting the task:\n%s", code, daemonErr.String())
	case <-time.After(limit):
		_, log := stopDaemon()
		t.Fatalf("the daemon did not start the approved plan's first task within %s. It would need a restart to see the project.\ndaemon log:\n%s", limit, log)
	}

	t.Logf("the running daemon launched task %s in session %s for project %s (%s)", got.Task, got.Session, got.Project.Name, got.Project.Root)

	code, log := stopDaemon()
	t.Logf("afk daemon (one run, exit %d):\n%s", code, log)
	if code != 0 {
		t.Errorf("the daemon exited %d, want 0", code)
	}
	if got.Task != "T1.1" || got.Session != "t1-1" || got.Project.Root != root || got.Project.Name != "recipes" {
		t.Errorf("launch = %+v, want T1.1 in session t1-1 for recipes at %s", got, root)
	}
	if len(r.launches) != 1 {
		t.Errorf("the daemon launched %d times, want once: %+v", len(r.launches), r.launches)
	}
	// One start, in this order: nothing to build, then the project appears,
	// with no second "started" line between them.
	if n := strings.Count(log, "afk daemon started"); n != 1 {
		t.Errorf("the log has %d start lines, want 1", n)
	}
	none := strings.Index(log, "No projects yet.")
	appeared := strings.Index(log, "afk: now running recipes ("+root+")")
	if none < 0 || appeared < 0 || appeared < none {
		t.Errorf("the log does not say \"No projects yet.\" and then \"afk: now running recipes\":\n%s", log)
	}
	if !strings.Contains(log, "0 project(s)") {
		t.Errorf("the start line does not say the daemon started with 0 projects:\n%s", log)
	}

	// What approve left: the project is in the config (the comment above it
	// untouched), the plan is approved, and the ledger the launch was decided
	// from is the one approve made.
	cfg, err := daemonconfig.Load(env.config)
	if err != nil || len(cfg.Projects) != 1 || cfg.Projects[0].Path != root {
		t.Errorf("the config lists %+v (err %v), want the one project at %s", cfg.Projects, err, root)
	}
	if text, err := os.ReadFile(r.configPath()); err != nil || !strings.HasPrefix(string(text), "# my afk settings\n[daemon]\ntick = \"1s\"\n") {
		t.Errorf("the config's own lines were not kept (err %v):\n%s", err, text)
	}
	if st, ok, err := plansession.ReadState(root); err != nil || !ok || st.Phase != plansession.PhaseApproved {
		t.Errorf("planning session = %+v ok=%v err=%v, want phase approved", st, ok, err)
	}
	if !ledger.Exists(root) {
		t.Error("the project has no ledger")
	}
}
