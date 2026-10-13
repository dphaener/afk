package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/daemonconfig"
	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/planlint"
	"github.com/dphaener/afk/internal/plansession"
	"github.com/dphaener/afk/internal/projectconfig"
	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/ship"
)

// approveGit is a scripted git for `afk plan approve`: it records every call
// and answers the few commands the command runs. dirty is what `git status`
// answers for the documents; a commit that works clears it, as a real commit
// would, so a second run against the same fake finds nothing to commit.
type approveGit struct {
	dirty       bool
	origin      bool
	detached    bool
	statusFails bool
	commitFails bool
	pushFails   bool
	calls       [][]string
}

func (g *approveGit) run(dir, name string, args ...string) (string, error) {
	argv := append([]string{name}, args...)
	g.calls = append(g.calls, argv)
	line := strings.Join(argv, " ")
	exit := func(code int, stderr string) (string, error) {
		return "", &ship.CmdError{Cmd: line, Code: code, Stderr: stderr, Err: fmt.Errorf("exit status %d", code)}
	}
	switch {
	case strings.HasPrefix(line, "git status --porcelain -- "):
		if g.statusFails {
			return exit(128, "fatal: not a git repository")
		}
		if g.dirty {
			return "?? docs/\n", nil
		}
		return "", nil
	case strings.HasPrefix(line, "git add -- "):
		return "", nil
	case strings.HasPrefix(line, "git commit "):
		if g.commitFails {
			return exit(128, "fatal: unable to auto-detect email address")
		}
		g.dirty = false
		return "", nil
	case line == "git remote get-url origin":
		if !g.origin {
			return exit(2, "error: No such remote 'origin'")
		}
		return "git@example.test:me/app.git\n", nil
	case line == "git branch --show-current":
		if g.detached {
			return "\n", nil
		}
		return "main\n", nil
	case strings.HasPrefix(line, "git push "):
		if g.pushFails {
			return exit(1, "remote: permission denied")
		}
		return "", nil
	}
	return exit(1, "the test's git does not know this command")
}

// verbs returns the git subcommand of every call that asks about or changes
// the documents (status, add, commit, push), in order. The two questions
// about where to push (remote, branch) are left out: they change nothing.
func (g *approveGit) verbs() []string {
	var out []string
	for _, c := range g.calls {
		switch c[1] {
		case "status", "add", "commit", "push":
			out = append(out, c[1])
		}
	}
	return out
}

// pathsOf returns the paths after "--" of the first call of a git subcommand.
func (g *approveGit) pathsOf(verb string) []string {
	for _, c := range g.calls {
		if c[1] != verb {
			continue
		}
		for i, a := range c {
			if a == "--" {
				return c[i+1:]
			}
		}
	}
	return nil
}

// approveRig is one project and one machine as `afk plan approve` sees them:
// a temp project folder, a temp state folder, the machine config held in
// memory (projectRig) and a scripted git. failStep makes that step's seam
// fail, for the crash matrix.
type approveRig struct {
	t        *testing.T
	root     string
	stateDir string
	git      *approveGit
	plan     *planFake
	cfg      *projectRig
	failStep approveStep
	daemon   bool
}

func newApproveRig(t *testing.T) *approveRig {
	t.Helper()
	root := t.TempDir()
	cfg := newProjectRig()
	cfg.dirs[root], cfg.repos[root] = true, true
	return &approveRig{
		t:        t,
		root:     root,
		stateDir: t.TempDir(),
		git:      &approveGit{dirty: true, origin: true},
		plan:     &planFake{commonDir: filepath.Join(root, ".git")},
		cfg:      cfg,
	}
}

// seed writes a planning session with every file the earlier phases need and
// a plan the daemon can read, recorded at the given mode and phase.
func (r *approveRig) seed(mode, phase string) {
	r.t.Helper()
	writePlanFile(r.t, r.root, ".afk/plan/state.json", `{"mode":"`+mode+`","phase":"`+phase+`","updated_at":"2026-10-12T00:00:00Z"}`)
	writePlanFile(r.t, r.root, ".afk/plan/brief.md", "a brief\n")
	writePlanFile(r.t, r.root, ".afk/plan/survey.md", "a survey\n")
	writePlanFile(r.t, r.root, ".afk/plan/slices.md", "slices\n")
	writePlanFile(r.t, r.root, ".afk/plan/critic.md", "critic\n")
	writePlanFile(r.t, r.root, ".afk/plan/questions.jsonl", planQuestions("no: a command line"))
	writePlanFile(r.t, r.root, "docs/PLAN.md", daemonTestPlan)
	writePlanFile(r.t, r.root, ".gitignore", ".afk/\nafk.local.toml\n")
}

func (r *approveRig) deps() approveDeps {
	env := r.cfg.env()
	write := env.write
	env.write = func(p string, data []byte) error {
		if r.failStep == approveProject {
			return errors.New("the test's config folder is read-only")
		}
		return write(p, data)
	}
	return approveDeps{
		plan:   r.plan.deps(r.root),
		run:    r.git.run,
		exists: fileExists,
		create: func(path string, data []byte) error {
			if r.failStep == approveWriteConfig {
				return errors.New("the test's disk is full")
			}
			return createExclusive(path, data)
		},
		project:      env,
		stateDir:     func() (string, error) { return r.stateDir, nil },
		ledgerExists: ledger.Exists,
		ledgerInit: func(root string, done []string) error {
			if r.failStep == approveLedger {
				return errors.New("the test's ledger folder is read-only")
			}
			return ledger.Init(root, done)
		},
		writeState: func(root string, s plansession.State) error {
			if r.failStep == approvePhase {
				return errors.New("the test's planning folder is read-only")
			}
			return plansession.WriteState(root, s)
		},
		daemonRunning: func(string) bool { return r.daemon },
	}
}

func (r *approveRig) approve(args ...string) (code int, out, errOut string) {
	r.t.Helper()
	// commitFails follows failStep so the crash matrix has one switch.
	r.git.commitFails = r.failStep == approveCommit
	var so, se bytes.Buffer
	code = runPlanApprove(args, &so, &se, r.deps())
	return code, so.String(), se.String()
}

func (r *approveRig) phase() string {
	r.t.Helper()
	st, ok, err := plansession.ReadState(r.root)
	if err != nil || !ok {
		r.t.Fatalf("reading state.json: ok=%v err=%v", ok, err)
	}
	return st.Phase
}

// listed says whether the machine config lists the project.
func (r *approveRig) listed() bool {
	r.t.Helper()
	cfg, err := daemonconfig.Load(r.cfg.env().Deps)
	if err != nil {
		r.t.Fatalf("the machine config does not load: %v", err)
	}
	for _, p := range cfg.Projects {
		if p.Path == r.root {
			return true
		}
	}
	return false
}

func (r *approveRig) read(rel string) string {
	r.t.Helper()
	data, err := os.ReadFile(filepath.Join(r.root, filepath.FromSlash(rel)))
	if err != nil {
		r.t.Fatal(err)
	}
	return string(data)
}

// approveStepLine is the start of the line each step prints when it did
// something, so a test can say which steps a run did.
var approveStepLine = map[approveStep]string{
	approveWriteConfig: "Wrote afk.toml",
	approveCommit:      "Committed the plan",
	approveLedger:      "Created the task ledger",
	approveProject:     "Added ",
	approvePhase:       "Marked the planning session approved",
	approveDropReview:  "Took the plan off the list",
}

// stepsDone lists the steps whose line is in a run's stdout.
func stepsDone(stdout string) []approveStep {
	var done []approveStep
	for s := approveWriteConfig; s <= approveDropReview; s++ {
		for _, line := range strings.Split(stdout, "\n") {
			if strings.HasPrefix(line, approveStepLine[s]) {
				done = append(done, s)
				break
			}
		}
	}
	return done
}

// TestApproveSteps is the whole decision of which steps a run does: one row
// per fact, and the two ends.
//
// Load-bearing rows:
//   - "everything done": no steps at all. This is what makes a second run
//     harmless.
//   - "nothing done": all five in the fixed order. The order is the safety:
//     the ledger before the machine config (the daemon may tick the project
//     the moment it is listed) and the phase after both.
//   - "no afk.toml, documents not asked about": the commit is planned even
//     though git reported no change, because git was asked before afk.toml
//     was written. Without it a new project's afk.toml would be written and
//     never committed.
//   - "only the review entry left": a run that crashed after writing the
//     phase is not this row in practice (the gate answers "already approved"
//     first), but the decision itself must not tie the entry to the phase.
func TestApproveSteps(t *testing.T) {
	done := approveFacts{HasConfig: true, HasLedger: true, Listed: true, Approved: true}
	with := func(change func(*approveFacts)) approveFacts {
		f := done
		change(&f)
		return f
	}
	tests := []struct {
		name  string
		facts approveFacts
		want  []approveStep
	}{
		{"everything done", done, nil},
		{"nothing done", approveFacts{DocsChanged: true},
			[]approveStep{approveWriteConfig, approveCommit, approveLedger, approveProject, approvePhase}},
		{"nothing done, on the review list", approveFacts{DocsChanged: true, Reviewed: true},
			[]approveStep{approveWriteConfig, approveCommit, approveLedger, approveProject, approvePhase, approveDropReview}},
		{"no afk.toml, documents not asked about", with(func(f *approveFacts) { f.HasConfig = false }),
			[]approveStep{approveWriteConfig, approveCommit}},
		{"documents changed", with(func(f *approveFacts) { f.DocsChanged = true }), []approveStep{approveCommit}},
		{"no ledger", with(func(f *approveFacts) { f.HasLedger = false }), []approveStep{approveLedger}},
		{"not in the machine config", with(func(f *approveFacts) { f.Listed = false }), []approveStep{approveProject}},
		{"phase not written", with(func(f *approveFacts) { f.Approved = false }), []approveStep{approvePhase}},
		{"only the review entry left", with(func(f *approveFacts) { f.Reviewed = true }), []approveStep{approveDropReview}},
		{"crash after the commit", approveFacts{HasConfig: true},
			[]approveStep{approveLedger, approveProject, approvePhase}},
		{"crash after the ledger", approveFacts{HasConfig: true, HasLedger: true},
			[]approveStep{approveProject, approvePhase}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := approveSteps(tt.facts); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("approveSteps(%+v) = %v, want %v", tt.facts, got, tt.want)
			}
		})
	}
}

// TestPlanApproveRefuses pins the gate: every row exits without running a
// step, and the project, the machine config and git are exactly as they were.
//
// Load-bearing rows:
//   - one row per phase before review: a plan nobody has read, or that the
//     checker or the critic has not passed, must not start a build.
//   - "checker errors": a session recorded at review whose plan has since
//     gained an error. The advice is `afk plan lint`, not "finish check".
//   - "change session": its changes are in the plan already; approving would
//     commit and list a project for no reason.
//   - "the daemon would refuse the project": the planning session's gates all
//     pass, and the daemon's own reading of the project (here its ledger)
//     does not. Listing it would give the daemon an error on every pass.
//   - "machine config unreadable": step 4 would fail after the commit was
//     made, so it is refused while nothing has changed.
func TestPlanApproveRefuses(t *testing.T) {
	type row struct {
		name     string
		setup    func(r *approveRig)
		args     []string
		wantCode int
		wantErr  []string
	}
	var tests []row
	for _, phase := range plansession.PhasesFor(plansession.ModeNew) {
		if phase == plansession.PhaseReview {
			break
		}
		tests = append(tests, row{
			name:     "at the " + phase + " phase",
			setup:    func(r *approveRig) { r.seed(plansession.ModeNew, phase) },
			wantCode: 1,
			wantErr:  []string{"the session is at the " + phase + " phase, not review", planNothingChanged},
		})
	}
	tests = append(tests,
		row{
			name: "checker errors",
			setup: func(r *approveRig) {
				r.seed(plansession.ModeNew, plansession.PhaseReview)
				r.plan.lint = 2
			},
			wantCode: 1,
			wantErr:  []string{"2 errors", "afk plan lint", planNothingChanged},
		},
		row{
			name:     "change session",
			setup:    func(r *approveRig) { r.seed(plansession.ModeAmend, plansession.PhaseBrief) },
			wantCode: 1,
			wantErr:  []string{"changes a plan that already exists", "nothing to approve", planNothingChanged},
		},
		row{
			name:     "no session",
			setup:    func(r *approveRig) {},
			wantCode: 1,
			wantErr:  []string{"There is no planning session here", planNothingChanged},
		},
		row{
			name: "damaged state",
			setup: func(r *approveRig) {
				writePlanFile(r.t, r.root, ".afk/plan/state.json", "{not json")
			},
			wantCode: 1,
			wantErr:  []string{"state.json", planNothingChanged},
		},
		row{
			name: "the daemon would refuse the project",
			setup: func(r *approveRig) {
				r.seed(plansession.ModeNew, plansession.PhaseReview)
				writePlanFile(r.t, r.root, ".afk/build/ledger.json", "{not json")
			},
			wantCode: 1,
			wantErr:  []string{"The daemon could not build from this project", "ledger.json", planNothingChanged},
		},
		row{
			name: "not a git repository",
			setup: func(r *approveRig) {
				r.seed(plansession.ModeNew, plansession.PhaseReview)
				r.plan.commonDir = ""
			},
			wantCode: 1,
			wantErr:  []string{"is not a git repository", "git init", planNothingChanged},
		},
		row{
			name: "machine config unreadable",
			setup: func(r *approveRig) {
				r.seed(plansession.ModeNew, plansession.PhaseReview)
				r.cfg.text, r.cfg.exists = "[[project\n", true
			},
			wantCode: 1,
			wantErr:  []string{"afk's config cannot be read", planNothingChanged},
		},
		row{
			name: "git cannot answer",
			setup: func(r *approveRig) {
				r.seed(plansession.ModeNew, plansession.PhaseReview)
				writePlanFile(r.t, r.root, "afk.toml", "plan = \"docs/PLAN.md\"\n")
				r.git.statusFails = true
			},
			wantCode: 1,
			wantErr:  []string{"git could not say whether the plan is committed", planNothingChanged},
		},
		row{name: "unknown flag", setup: func(r *approveRig) {}, args: []string{"--bogus"}, wantCode: 2, wantErr: []string{planApproveUsageLine}},
		row{name: "unexpected word", setup: func(r *approveRig) {}, args: []string{"now"}, wantCode: 2, wantErr: []string{`unexpected word "now"`}},
	)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newApproveRig(t)
			tt.setup(r)
			before := folderEntries(t, r.root)
			stateBefore, _ := os.ReadFile(plansession.StatePath(r.root))
			cfgBefore := r.cfg.text

			code, out, errOut := r.approve(tt.args...)
			if code != tt.wantCode {
				t.Fatalf("exit = %d, want %d; stdout: %s stderr: %s", code, tt.wantCode, out, errOut)
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(errOut, want) {
					t.Errorf("stderr missing %q:\n%s", want, errOut)
				}
			}
			if done := stepsDone(out); len(done) != 0 {
				t.Errorf("a refused approve did steps %v:\n%s", done, out)
			}
			for _, v := range r.git.verbs() {
				if v != "status" {
					t.Errorf("a refused approve ran git %s; calls: %v", v, r.git.calls)
				}
			}
			if after := folderEntries(t, r.root); !reflect.DeepEqual(before, after) {
				t.Errorf("a refused approve changed the project folder:\nbefore %v\nafter  %v", before, after)
			}
			if stateAfter, _ := os.ReadFile(plansession.StatePath(r.root)); !bytes.Equal(stateBefore, stateAfter) {
				t.Errorf("a refused approve changed state.json:\nbefore %q\nafter  %q", stateBefore, stateAfter)
			}
			if r.cfg.writes != 0 || r.cfg.text != cfgBefore {
				t.Errorf("a refused approve wrote the machine config (%d writes)", r.cfg.writes)
			}
		})
	}
}

// TestPlanApproveHappyPath is the whole command on a new project with an
// origin and a review-list entry, and then the same command again.
//
// Load-bearing checks: git is asked and told exactly status, add, commit,
// push, with only the documents after "--" (so nothing else in the checkout
// can be committed, and no flag such as --no-verify, -A or --force is ever
// passed); the ledger exists and the machine config lists the project; the
// phase is approved with the clock's time and the mode kept; and the second
// run prints the already-approved line and touches nothing, git included.
func TestPlanApproveHappyPath(t *testing.T) {
	r := newApproveRig(t)
	r.seed(plansession.ModeNew, plansession.PhaseReview)
	r.daemon = true
	// A file that is not a document, to show it is never staged.
	writePlanFile(t, r.root, "notes.txt", "private\n")
	if err := daemonstate.AddReview(r.stateDir, daemonstate.Review{Root: r.root, At: "2026-10-12T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := r.approve()
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stdout: %s stderr: %s", code, out, errOut)
	}
	if errOut != "" {
		t.Errorf("stderr = %q, want nothing", errOut)
	}

	docs := []string{"docs/PLAN.md", "afk.toml", ".gitignore"}
	if got, want := r.git.verbs(), []string{"status", "add", "commit", "push"}; !reflect.DeepEqual(got, want) {
		t.Errorf("git verbs = %v, want %v; calls: %v", got, want, r.git.calls)
	}
	wantCalls := [][]string{
		append([]string{"git", "status", "--porcelain", "--"}, docs...),
		append([]string{"git", "add", "--"}, docs...),
		append([]string{"git", "commit", "-m", planApproveSubject, "--"}, docs...),
		{"git", "remote", "get-url", "origin"},
		{"git", "branch", "--show-current"},
		{"git", "push", "origin", "main"},
	}
	if !reflect.DeepEqual(r.git.calls, wantCalls) {
		t.Errorf("git calls:\n got %v\nwant %v", r.git.calls, wantCalls)
	}

	if got, want := r.read("afk.toml"), "plan = \"docs/PLAN.md\"\n"; got != want {
		t.Errorf("afk.toml = %q, want %q", got, want)
	}
	if !ledger.Exists(r.root) {
		t.Error("no ledger was created")
	}
	if l, err := ledger.Load(r.root); err != nil || len(l.Tasks) != 0 {
		t.Errorf("ledger = %+v, err %v; want an empty ledger", l, err)
	}
	if !r.listed() {
		t.Errorf("the machine config does not list the project:\n%s", r.cfg.text)
	}
	st, _, err := plansession.ReadState(r.root)
	if err != nil {
		t.Fatal(err)
	}
	want := plansession.State{Mode: plansession.ModeNew, Phase: plansession.PhaseApproved, UpdatedAt: plansession.FormatTime(planFakeNow)}
	if st != want {
		t.Errorf("state = %+v, want %+v", st, want)
	}
	if reviews := daemonstate.ReadReviews(r.stateDir); len(reviews) != 0 {
		t.Errorf("the review list still has %v", reviews)
	}
	if got, want := stepsDone(out), []approveStep{approveWriteConfig, approveCommit, approveLedger, approveProject, approvePhase, approveDropReview}; !reflect.DeepEqual(got, want) {
		t.Errorf("steps done = %v, want %v:\n%s", got, want, out)
	}
	// The counts are the checker's own, so the line is built from them here
	// too: a count made by hand in the command would not match.
	sum := planlint.Lint([]byte(daemonTestPlan)).Summary
	if sum.Tasks != 2 {
		t.Fatalf("the fixture plan has %d tasks by the checker's count, want 2", sum.Tasks)
	}
	for _, want := range []string{
		"Pushed the commit to origin (main).",
		fmt.Sprintf("Approved the plan at docs/PLAN.md: %s in %s, %s for you to do, %s.",
			plural(sum.Tasks, "task"), plural(sum.Milestones, "milestone"), plural(sum.OwnerTasks, "task"), plural(sum.OpenQuestions, "open question")),
		"A running afk daemon starts the first ready task within a few seconds.",
		"afk clean master-plan",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q:\n%s", want, out)
		}
	}

	// The second run.
	calls, writes := len(r.git.calls), r.cfg.writes
	before := folderEntries(t, r.root)
	stateBefore := r.read(".afk/plan/state.json")
	code, out, errOut = r.approve()
	if code != 0 || out != "The plan is already approved. Nothing was changed.\n" || errOut != "" {
		t.Errorf("second run: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	if len(r.git.calls) != calls {
		t.Errorf("the second run ran git: %v", r.git.calls[calls:])
	}
	if r.cfg.writes != writes {
		t.Errorf("the second run wrote the machine config")
	}
	if after := folderEntries(t, r.root); !reflect.DeepEqual(before, after) {
		t.Errorf("the second run changed the project folder:\nbefore %v\nafter  %v", before, after)
	}
	if got := r.read(".afk/plan/state.json"); got != stateBefore {
		t.Errorf("the second run rewrote state.json:\nbefore %q\nafter  %q", stateBefore, got)
	}
}

// TestPlanApproveCrashMatrix fails each of the five steps in turn, then runs
// the command again with nothing failing.
//
// Every row is load-bearing, and for the same two reasons. After the failed
// run the phase is still review, so the session is never "approved" with a
// step missing; this is what writing the phase last buys. And the second run
// does exactly the steps from the failed one on, no earlier one again (no
// second commit, no second ledger), and ends approved; this is what deciding
// each step from its own fact buys. The report of the failed run is checked
// too: it must name the step, list what is left and say to run it again.
func TestPlanApproveCrashMatrix(t *testing.T) {
	all := []approveStep{approveWriteConfig, approveCommit, approveLedger, approveProject, approvePhase}
	for i, failed := range all {
		t.Run("step "+fmt.Sprint(int(failed))+" fails: "+failed.todo(), func(t *testing.T) {
			r := newApproveRig(t)
			r.seed(plansession.ModeNew, plansession.PhaseReview)
			r.failStep = failed

			code, out, errOut := r.approve()
			if code != 1 {
				t.Fatalf("failed run: exit = %d, want 1; stdout: %s stderr: %s", code, out, errOut)
			}
			if got := r.phase(); got != plansession.PhaseReview {
				t.Errorf("phase after the failed run = %q, want review", got)
			}
			if got, want := stepsDone(out), all[:i]; !reflect.DeepEqual(got, want) && (len(got) != 0 || len(want) != 0) {
				t.Errorf("failed run did steps %v, want %v:\n%s", got, want, out)
			}
			for _, want := range []string{
				"afk plan approve: could not " + failed.todo() + ".",
				"Not done: " + failed.todo(),
				"The plan is not approved yet.",
				"Fix that and run afk plan approve again.",
			} {
				if !strings.Contains(errOut, want) {
					t.Errorf("failed run: stderr missing %q:\n%s", want, errOut)
				}
			}
			if strings.Contains(errOut, planNothingChanged) && i > 0 {
				t.Errorf("failed run claims nothing was changed after doing %v:\n%s", all[:i], errOut)
			}
			// The machine config is never written before the ledger exists.
			if r.listed() && !ledger.Exists(r.root) {
				t.Error("the project is in the machine config with no ledger")
			}
			if failed <= approveProject && r.listed() {
				t.Error("the project was listed by a run that failed at or before that step")
			}

			r.failStep = 0
			code, out, errOut = r.approve()
			if code != 0 {
				t.Fatalf("second run: exit = %d, want 0; stdout: %s stderr: %s", code, out, errOut)
			}
			if got, want := stepsDone(out), all[i:]; !reflect.DeepEqual(got, want) {
				t.Errorf("second run did steps %v, want %v:\n%s", got, want, out)
			}
			if got := r.phase(); got != plansession.PhaseApproved {
				t.Errorf("phase after the second run = %q, want approved", got)
			}
			if !ledger.Exists(r.root) || !r.listed() || r.read("afk.toml") == "" {
				t.Errorf("the second run left something undone: ledger %v, listed %v", ledger.Exists(r.root), r.listed())
			}
			commits := 0
			for _, c := range r.git.calls {
				if c[1] == "commit" {
					commits++
				}
			}
			// One commit that worked; a failed attempt at step 2 is a second call.
			wantCommits := 1
			if failed == approveCommit {
				wantCommits = 2
			}
			if commits != wantCommits {
				t.Errorf("git commit was run %d times over both runs, want %d; calls: %v", commits, wantCommits, r.git.calls)
			}
		})
	}
}

// TestPlanApproveVariants covers what the commit and the push do in each
// kind of project. Every row ends approved with exit 0.
//
// Load-bearing rows:
//   - "via_pr": no push at all, because in such a project a push to the base
//     branch can be a deploy.
//   - "push fails": still approved. The commit is made and the daemon builds
//     from the local checkout, so a failed push is a note with the command.
//   - "afk.toml exists": it is byte-identical afterwards. The owner's file is
//     never rewritten.
//   - "every document present": the design brief and the agent instruction
//     files are committed when they are there, and a source file and a
//     private note beside them are not. The instruction file names come from
//     internal/provider, so this file names no agent.
//   - "documents already committed": no add, no commit and no push, so a
//     project whose plan was committed by hand is not given an empty commit.
func TestPlanApproveVariants(t *testing.T) {
	everyDoc := append([]string{"docs/PLAN.md", "afk.toml", ".gitignore", "CONTEXT.md", plansession.DesignBriefFile}, provider.InstructionFiles()...)
	tests := []struct {
		name      string
		setup     func(r *approveRig)
		wantVerbs []string
		wantDocs  []string
		wantOut   []string
		wantErr   []string
		check     func(t *testing.T, r *approveRig)
	}{
		{
			name:      "no origin",
			setup:     func(r *approveRig) { r.git.origin = false },
			wantVerbs: []string{"status", "add", "commit"},
			wantDocs:  []string{"docs/PLAN.md", "afk.toml", ".gitignore"},
			wantOut:   []string{"The repository has no origin, so the commit was not pushed."},
		},
		{
			name: "via_pr",
			setup: func(r *approveRig) {
				writePlanFile(r.t, r.root, "afk.toml", "plan = \"docs/PLAN.md\"\n\n[planning]\nvia_pr = true\n")
			},
			wantVerbs: []string{"status", "add", "commit"},
			wantDocs:  []string{"docs/PLAN.md", "afk.toml", ".gitignore"},
			wantOut:   []string{"The commit was not pushed", "first task's pull request"},
			check: func(t *testing.T, r *approveRig) {
				for _, c := range r.git.calls {
					if c[1] == "remote" || c[1] == "branch" {
						t.Errorf("via_pr still asked git where to push: %v", c)
					}
				}
			},
		},
		{
			name: "via_pr in afk.local.toml, no afk.toml",
			setup: func(r *approveRig) {
				writePlanFile(r.t, r.root, projectconfig.LocalFileName, "[planning]\nvia_pr = true\n")
			},
			wantVerbs: []string{"status", "add", "commit"},
			wantDocs:  []string{"docs/PLAN.md", "afk.toml", ".gitignore"},
			wantOut:   []string{"Wrote afk.toml", "The commit was not pushed"},
		},
		{
			name:      "push fails",
			setup:     func(r *approveRig) { r.git.pushFails = true },
			wantVerbs: []string{"status", "add", "commit", "push"},
			wantDocs:  []string{"docs/PLAN.md", "afk.toml", ".gitignore"},
			wantErr:   []string{"Note: the commit could not be pushed", "permission denied", "push origin main"},
		},
		{
			name:      "detached HEAD",
			setup:     func(r *approveRig) { r.git.detached = true },
			wantVerbs: []string{"status", "add", "commit"},
			wantDocs:  []string{"docs/PLAN.md", "afk.toml", ".gitignore"},
			wantErr:   []string{"Note: the commit was not pushed, because the project is not on a branch"},
		},
		{
			name: "afk.toml exists",
			setup: func(r *approveRig) {
				writePlanFile(r.t, r.root, "afk.toml", "# mine\nplan = \"docs/PLAN.md\"\n")
			},
			wantVerbs: []string{"status", "add", "commit", "push"},
			wantDocs:  []string{"docs/PLAN.md", "afk.toml", ".gitignore"},
			check: func(t *testing.T, r *approveRig) {
				if got, want := r.read("afk.toml"), "# mine\nplan = \"docs/PLAN.md\"\n"; got != want {
					t.Errorf("afk.toml was rewritten: %q, want %q", got, want)
				}
			},
		},
		{
			name: "plan somewhere else",
			setup: func(r *approveRig) {
				writePlanFile(r.t, r.root, "afk.toml", "plan = \"PLAN.md\"\n")
				writePlanFile(r.t, r.root, "PLAN.md", daemonTestPlan)
			},
			wantVerbs: []string{"status", "add", "commit", "push"},
			wantDocs:  []string{"PLAN.md", "afk.toml", ".gitignore"},
			wantOut:   []string{"Approved the plan at PLAN.md"},
		},
		{
			name: "every document present",
			setup: func(r *approveRig) {
				for _, doc := range everyDoc[3:] {
					writePlanFile(r.t, r.root, doc, "text\n")
				}
				writePlanFile(r.t, r.root, "src/main.go", "package main\n")
				writePlanFile(r.t, r.root, "notes.txt", "private\n")
				writePlanFile(r.t, r.root, projectconfig.LocalFileName, "[notify]\nntfy_url = \"\"\n")
			},
			wantVerbs: []string{"status", "add", "commit", "push"},
			wantDocs:  everyDoc,
		},
		{
			name: "documents already committed",
			setup: func(r *approveRig) {
				writePlanFile(r.t, r.root, "afk.toml", "plan = \"docs/PLAN.md\"\n")
				r.git.dirty = false
			},
			wantVerbs: []string{"status"},
			check: func(t *testing.T, r *approveRig) {
				if len(r.git.calls) != 1 {
					t.Errorf("git calls = %v, want the one status", r.git.calls)
				}
			},
		},
		{
			name: "ledger already there",
			setup: func(r *approveRig) {
				if err := ledger.Init(r.root, []string{"T1.1"}); err != nil {
					r.t.Fatal(err)
				}
			},
			wantVerbs: []string{"status", "add", "commit", "push"},
			wantDocs:  []string{"docs/PLAN.md", "afk.toml", ".gitignore"},
			check: func(t *testing.T, r *approveRig) {
				// The owner's record of what is built is kept, not replaced.
				if l, err := ledger.Load(r.root); err != nil || l.Status("T1.1") != ledger.Done {
					t.Errorf("the ledger was replaced: %+v, err %v", l, err)
				}
			},
		},
		{
			name: "already in the machine config",
			setup: func(r *approveRig) {
				r.cfg.text, r.cfg.exists = "# my projects\n[[project]]\npath = \""+r.root+"\"\nname = \"mine\"\n", true
			},
			wantVerbs: []string{"status", "add", "commit", "push"},
			wantDocs:  []string{"docs/PLAN.md", "afk.toml", ".gitignore"},
			check: func(t *testing.T, r *approveRig) {
				if r.cfg.writes != 0 {
					t.Errorf("the machine config was written %d times, want 0", r.cfg.writes)
				}
			},
		},
		{
			name:      "no daemon running",
			setup:     func(r *approveRig) {},
			wantVerbs: []string{"status", "add", "commit", "push"},
			wantDocs:  []string{"docs/PLAN.md", "afk.toml", ".gitignore"},
			wantOut:   []string{"Start it with afk daemon"},
		},
	}
	allowed := map[string]bool{}
	for _, d := range everyDoc {
		allowed[d] = true
	}
	allowed["PLAN.md"] = true
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newApproveRig(t)
			r.seed(plansession.ModeNew, plansession.PhaseReview)
			tt.setup(r)

			code, out, errOut := r.approve()
			if code != 0 {
				t.Fatalf("exit = %d, want 0; stdout: %s stderr: %s", code, out, errOut)
			}
			if got := r.phase(); got != plansession.PhaseApproved {
				t.Errorf("phase = %q, want approved", got)
			}
			if !ledger.Exists(r.root) || !r.listed() {
				t.Errorf("ledger %v, listed %v; want both", ledger.Exists(r.root), r.listed())
			}
			if got := r.git.verbs(); !reflect.DeepEqual(got, tt.wantVerbs) {
				t.Errorf("git verbs = %v, want %v; calls: %v", got, tt.wantVerbs, r.git.calls)
			}
			for _, verb := range []string{"add", "commit"} {
				if got := r.git.pathsOf(verb); !reflect.DeepEqual(got, tt.wantDocs) {
					t.Errorf("git %s paths = %v, want %v", verb, got, tt.wantDocs)
				}
			}
			for _, c := range r.git.calls {
				for _, a := range c {
					if a == "-A" || a == "--all" || a == "--no-verify" || a == "-n" || a == "--force" || a == "-f" || strings.HasPrefix(a, "--force") {
						t.Errorf("git was given %q: %v", a, c)
					}
				}
			}
			for _, verb := range []string{"status", "add", "commit"} {
				for _, p := range r.git.pathsOf(verb) {
					if !allowed[p] {
						t.Errorf("git %s was given %q, which is not one of the documents", verb, p)
					}
				}
			}
			for _, want := range tt.wantOut {
				if !strings.Contains(out, want) {
					t.Errorf("stdout missing %q:\n%s", want, out)
				}
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(errOut, want) {
					t.Errorf("stderr missing %q:\n%s", want, errOut)
				}
			}
			if len(tt.wantErr) == 0 && errOut != "" {
				t.Errorf("stderr = %q, want nothing", errOut)
			}
			if tt.check != nil {
				tt.check(t, r)
			}
		})
	}
}

// TestCreateExclusive pins the one property step 1 rests on: a file that is
// there is never overwritten, and the error says so in a way the step can
// tell from a real failure.
func TestCreateExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "afk.toml")
	if err := createExclusive(path, []byte("one\n")); err != nil {
		t.Fatal(err)
	}
	err := createExclusive(path, []byte("two\n"))
	if !errors.Is(err, os.ErrExist) {
		t.Errorf("second create: err = %v, want one that is fs.ErrExist", err)
	}
	if data, _ := os.ReadFile(path); string(data) != "one\n" {
		t.Errorf("the file was overwritten: %q", data)
	}
}

// TestApproveDocumentNames pins the list of what approve may ever commit.
// The "plan is also another document" row is load-bearing: git refuses
// nothing about a path given twice, but the commit line printed to the owner
// would list it twice.
func TestApproveDocumentNames(t *testing.T) {
	instructions := provider.InstructionFiles()
	tests := []struct {
		name    string
		planRel string
		want    []string
	}{
		{"the default plan", "docs/PLAN.md",
			append([]string{"docs/PLAN.md", "afk.toml", ".gitignore", "CONTEXT.md", "docs/design-brief.md"}, instructions...)},
		{"the plan is also another document", "CONTEXT.md",
			append([]string{"CONTEXT.md", "afk.toml", ".gitignore", "docs/design-brief.md"}, instructions...)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := approveDocumentNames(tt.planRel); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("approveDocumentNames(%q) = %v, want %v", tt.planRel, got, tt.want)
			}
		})
	}
}
