package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/daemon"
	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/plansession"
	"github.com/dphaener/afk/internal/usagestate"
	"github.com/dphaener/afk/internal/web"
	"github.com/dphaener/afk/internal/webpass"
)

// TestPlanFolderCheck pins the web page's read-only folder check: what kind
// each folder is, the path the card shows, the refusal sentence, and that the
// check changes nothing. Each row is then started for real (with fakes for
// git init and the host) in a copy of the same folder, and the outcome must be
// the one the kind promised, so the confirm card and Start planning agree.
//
// Load-bearing rows:
//   - "empty repository" and "plain folder": the two kinds the card offers to
//     start, told apart by whether planning will run git init. The plain row
//     must leave no .git behind: the check must never initialise.
//   - "open session, host running" and "open session, host gone": both are
//     open, and starting resumes; neither reseeds.
//   - "has a plan": refused here as has_plan (the page words it and points at
//     Change the plan), with no reason of its own.
//   - "missing folder" and "relative path": refused, the first naming the
//     folder, the second with no path at all, and nothing created. "missing
//     folder" and "a file" carry the page's own sentences, and no reason
//     may name --project, which only the command line has.
//   - "~/ is expanded": the card shows the full path, never the "~".
//   - "subfolder of a repository": the path is the main checkout, where
//     planning really runs.
func TestPlanFolderCheck(t *testing.T) {
	openSession := func(t *testing.T, dir string) {
		writePlanFile(t, dir, ".afk/plan/state.json", planState(plansession.PhaseRounds))
		writePlanFile(t, dir, ".afk/plan/brief.md", "a brief\n")
	}
	tests := []struct {
		name  string
		setup func(t *testing.T, dir string)
		// text may hold {dir}; "" means the folder itself.
		text    string
		notRepo bool
		alive   bool
		want    string
		// wantPath may hold {dir}; "-" means no path.
		wantPath   string
		wantReason string
		// wantStart is what Start planning does with the same folder.
		wantStart planSurfaceOutcome
	}{
		{name: "empty repository", want: web.PlanFolderRepo, wantStart: planSurfaceStarted},
		{name: "plain folder", notRepo: true, want: web.PlanFolderFolder, wantStart: planSurfaceStarted},
		{name: "open session, host running", setup: openSession, alive: true,
			want: web.PlanFolderOpen, wantStart: planSurfaceResumed},
		{name: "open session, host gone", setup: openSession,
			want: web.PlanFolderOpen, wantStart: planSurfaceResumed},
		{name: "has a plan", setup: func(t *testing.T, dir string) { writePlanFile(t, dir, "docs/PLAN.md", "# Plan\n") },
			want: web.PlanFolderHasPlan, wantStart: planSurfaceRefused},
		{name: "damaged state", setup: func(t *testing.T, dir string) { writePlanFile(t, dir, ".afk/plan/state.json", "{not json") },
			want: web.PlanFolderRefused, wantReason: "by hand", wantStart: planSurfaceRefused},
		{name: "missing folder", text: filepath.Join("{dir}", "gone"),
			want: web.PlanFolderRefused, wantPath: filepath.Join("{dir}", "gone"),
			wantReason: "There is no folder at " + filepath.Join("{dir}", "gone") + ". Type the path of a folder that exists, or a plain name", wantStart: planSurfaceRefused},
		{name: "a file", setup: func(t *testing.T, dir string) { writePlanFile(t, dir, "notes.txt", "x\n") },
			text: filepath.Join("{dir}", "notes.txt"), want: web.PlanFolderRefused,
			wantPath: filepath.Join("{dir}", "notes.txt"), wantReason: "is a file, not a folder. Type the path of a folder.", wantStart: planSurfaceRefused},
		{name: "relative path", text: "shop", want: web.PlanFolderRefused, wantPath: "-",
			wantReason: `the project folder "shop" is not a full path`, wantStart: planSurfaceRefused},
		{name: "~/ is expanded", setup: func(t *testing.T, dir string) { writePlanFile(t, dir, "x/keep", "") },
			text: "~/x", notRepo: true, want: web.PlanFolderFolder, wantPath: filepath.Join("{dir}", "x"), wantStart: planSurfaceStarted},
		{name: "subfolder of a repository", setup: func(t *testing.T, dir string) { writePlanFile(t, dir, "sub/keep", "") },
			text: filepath.Join("{dir}", "sub"), want: web.PlanFolderRepo, wantStart: planSurfaceStarted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// One folder for the check, an identical one for the start.
			run := func(dir string) (planFake, string) {
				if tt.setup != nil {
					tt.setup(t, dir)
				}
				f := planFake{alive: tt.alive, home: dir, commonDir: filepath.Join(dir, ".git")}
				if tt.notRepo {
					f.commonDir = ""
				}
				text := tt.text
				if text == "" {
					text = dir
				}
				return f, strings.ReplaceAll(text, "{dir}", dir)
			}

			dir := t.TempDir()
			f, text := run(dir)
			before := folderFiles(t, dir)
			got := webCheckPlanFolderWith(planCheckDeps(f.deps(t.TempDir())), text)

			if got.Kind != tt.want {
				t.Fatalf("kind = %q, want %q; reason: %s", got.Kind, tt.want, got.Reason)
			}
			wantPath := strings.ReplaceAll(tt.wantPath, "{dir}", dir)
			switch tt.wantPath {
			case "":
				wantPath = dir
			case "-":
				wantPath = ""
			}
			if got.Path != wantPath {
				t.Errorf("path = %q, want %q", got.Path, wantPath)
			}
			if want := strings.ReplaceAll(tt.wantReason, "{dir}", dir); !strings.Contains(got.Reason, want) {
				t.Errorf("reason missing %q:\n%s", want, got.Reason)
			}
			if strings.Contains(got.Reason, "--project") {
				t.Errorf("reason names a command flag the page has no use for: %s", got.Reason)
			}
			if (got.Kind == web.PlanFolderRefused) != (got.Reason != "") {
				t.Errorf("reason %q on kind %q: only a refusal has one", got.Reason, got.Kind)
			}
			if len(f.calls) != 0 {
				t.Errorf("the check called %v, want nothing", f.calls)
			}
			if after := folderFiles(t, dir); !reflect.DeepEqual(after, before) {
				t.Errorf("the check changed the folder:\nbefore %v\nafter  %v", before, after)
			}
			if _, err := os.Stat(filepath.Join(dir, ".git")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the check left %s behind (err %v)", filepath.Join(dir, ".git"), err)
			}

			startDir := t.TempDir()
			sf, startText := run(startDir)
			res := startPlanForSurface(planSurfaceRequest{Mode: planSurfaceNew, Project: startText}, sf.deps(t.TempDir()))
			if res.Outcome != tt.wantStart {
				t.Errorf("start outcome = %q (%s), want %q for kind %q", res.Outcome, res.Reason, tt.wantStart, got.Kind)
			}
		})
	}
}

// TestPlanFolderCheckWritersRefuse pins planCheckDeps: every writer the check
// is given refuses. The check never calls them; this is the guard against a
// later change that would, which would make the confirm card start a
// repository or a host before the owner agreed.
func TestPlanFolderCheckWritersRefuse(t *testing.T) {
	var f planFake
	d := planCheckDeps(f.deps(t.TempDir()))
	if err := d.gitInit(t.TempDir()); !errors.Is(err, errPlanCheckWrites) {
		t.Errorf("gitInit = %v, want the refusal", err)
	}
	if _, err := d.spawn(planHostSpawn(t.TempDir(), "")); !errors.Is(err, errPlanCheckWrites) {
		t.Errorf("spawn = %v, want the refusal", err)
	}
	if _, code, _ := d.attach("x", true); code == 0 {
		t.Error("attach succeeded, want a refusal")
	}
	if _, err := d.ask("?"); !errors.Is(err, errPlanCheckWrites) {
		t.Errorf("ask = %v, want the refusal", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("the fake's writers were reached: %v", f.calls)
	}
}

// TestBeingPlannedGone pins what "the planning session is gone" means for a
// being-planned entry. Pure.
//
// Load-bearing rows:
//   - "no state.json": the session was cleaned up; keeping it would list a
//     project whose chat has nothing behind it.
//   - "unreadable state.json": not gone. That project needs the owner, and
//     dropping it would hide it.
//   - "new at review": not gone. A new plan at review is waiting for approval,
//     not finished.
//   - "approved" and "finished change": gone (plansession.Finished).
func TestBeingPlannedGone(t *testing.T) {
	tests := []struct {
		name string
		ok   bool
		err  error
		st   plansession.State
		want bool
	}{
		{"no state.json", false, nil, plansession.State{}, true},
		{"unreadable state.json", false, errors.New("bad json"), plansession.State{}, false},
		{"new at brief", true, nil, plansession.State{Mode: plansession.ModeNew, Phase: plansession.PhaseBrief}, false},
		{"new at review", true, nil, plansession.State{Mode: plansession.ModeNew, Phase: plansession.PhaseReview}, false},
		{"approved", true, nil, plansession.State{Mode: plansession.ModeNew, Phase: plansession.PhaseApproved}, true},
		{"finished change", true, nil, plansession.State{Mode: plansession.ModeAmend, Phase: plansession.PhaseReview}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := beingPlannedGone(tt.ok, tt.err, tt.st); got != tt.want {
				t.Errorf("beingPlannedGone(%v, %v, %+v) = %v, want %v", tt.ok, tt.err, tt.st, got, tt.want)
			}
		})
	}
}

// beingPlannedRig is the daemon's real web deps over a temp state folder,
// with the start and the folder check pointed at a planFake, so a POST to
// /api/plan/new runs the real entry point, the real check and the real list
// writes without git, a host or an agent.
type beingPlannedRig struct {
	t        *testing.T
	stateDir string
	fake     *planFake
	deps     web.Deps
	handler  http.Handler
	now      time.Time
}

var beingPlannedNow = time.Date(2026, 10, 14, 9, 30, 0, 0, time.UTC)

func newBeingPlannedRig(t *testing.T, configured []daemon.Project) *beingPlannedRig {
	t.Helper()
	r := &beingPlannedRig{t: t, stateDir: t.TempDir(), fake: &planFake{}, now: beingPlannedNow}
	d := daemonWebDeps(func() []daemon.Project { return configured }, r.stateDir, usagestate.Deps{}, func() time.Time { return r.now })
	elsewhere := t.TempDir()
	d.StartPlan = func(root, mode, text string) web.PlanStartResult {
		return webStartPlanWith(startPlanForSurface, r.fake.deps(elsewhere), func(string) error { return nil }, root, mode, text)
	}
	d.CheckPlanFolder = func(text string) web.PlanFolderCheck {
		return webCheckPlanFolderWith(planCheckDeps(r.fake.deps(elsewhere)), text)
	}
	// The state deps above are zero, which would find the real state folder,
	// so the password is handed in here and no test reads the owner's file.
	d.Password = func() (webpass.Record, error) { return daemonTestWebRecord, nil }
	h, err := web.New(web.Config{}, d)
	if err != nil {
		t.Fatal(err)
	}
	r.deps, r.handler = d, h
	return r
}

// start sends POST /api/plan/new for dir, confirmed, as a repository.
func (r *beingPlannedRig) start(dir string) *httptest.ResponseRecorder {
	r.t.Helper()
	r.fake.commonDir = filepath.Join(dir, ".git")
	body := `{"path": ` + jsonString(dir) + `, "confirm": true}`
	req := httptest.NewRequest(http.MethodPost, "/api/plan/new", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "afk_web", Value: daemonTestWebCookie()})
	rec := httptest.NewRecorder()
	r.handler.ServeHTTP(rec, req)
	return rec
}

func jsonString(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}

// listed is the being-planned file as written, gone entries included.
func (r *beingPlannedRig) listed() []daemonstate.BeingPlanned {
	return daemonstate.ReadBeingPlanned(r.stateDir)
}

// shown is what the page is handed: the list with gone sessions dropped.
func (r *beingPlannedRig) shown() []web.BeingPlannedView {
	return r.deps.BeingPlanned()
}

// TestBeingPlannedLifecycle drives the being-planned list through the real
// page and the daemon's real wiring: what a start adds, what a refusal leaves
// alone, and when an entry stops being shown.
//
// Load-bearing rows:
//   - "started adds the folder" and "resumed keeps the name": the page sends
//     the owner to /p/<name>/plan/chat, so the name must be written on every
//     successful start and must not move when planning is resumed.
//   - "refused adds nothing" and "missing folder adds nothing": a refused
//     start must not leave a project on the overview.
//   - "gone when state.json is removed", "gone when approved": dropped from
//     what the page is shown; "kept when state.json is damaged": still shown.
//   - "the next start prunes gone entries": the file does not keep entries
//     whose session ended.
func TestBeingPlannedLifecycle(t *testing.T) {
	t.Run("started adds the folder", func(t *testing.T) {
		r := newBeingPlannedRig(t, nil)
		dir := filepath.Join(t.TempDir(), "shop")
		writePlanFile(t, dir, "keep", "")
		rec := r.start(dir)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"outcome":"started"`) {
			t.Fatalf("POST answered %d: %s", rec.Code, rec.Body)
		}
		want := []daemonstate.BeingPlanned{{Name: "shop", Root: dir, At: beingPlannedNow.Format(time.RFC3339)}}
		if got := r.listed(); !reflect.DeepEqual(got, want) {
			t.Errorf("list = %+v, want %+v", got, want)
		}
		if got := r.shown(); !reflect.DeepEqual(got, []web.BeingPlannedView{{Name: "shop", Root: dir}}) {
			t.Errorf("shown = %+v", got)
		}
		if !strings.Contains(rec.Body.String(), `"chat":"/p/shop/plan/chat"`) {
			t.Errorf("the answer does not lead to the chat: %s", rec.Body)
		}
	})

	t.Run("resumed keeps the name and moves the time", func(t *testing.T) {
		r := newBeingPlannedRig(t, nil)
		dir := filepath.Join(t.TempDir(), "shop")
		writePlanFile(t, dir, ".afk/plan/state.json", planState(plansession.PhaseRounds))
		if _, err := daemonstate.AddBeingPlanned(r.stateDir, daemonstate.BeingPlanned{Name: "my-shop", Root: dir, At: "2026-10-01T00:00:00Z"}, nil); err != nil {
			t.Fatal(err)
		}
		r.fake.alive = true
		rec := r.start(dir)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"outcome":"resumed"`) {
			t.Fatalf("POST answered %d: %s", rec.Code, rec.Body)
		}
		want := []daemonstate.BeingPlanned{{Name: "my-shop", Root: dir, At: beingPlannedNow.Format(time.RFC3339)}}
		if got := r.listed(); !reflect.DeepEqual(got, want) {
			t.Errorf("list = %+v, want %+v", got, want)
		}
	})

	t.Run("refused adds nothing", func(t *testing.T) {
		r := newBeingPlannedRig(t, nil)
		dir := t.TempDir()
		writePlanFile(t, dir, "docs/PLAN.md", "# Plan\n")
		if rec := r.start(dir); rec.Code != http.StatusBadRequest {
			t.Fatalf("POST answered %d, want 400: %s", rec.Code, rec.Body)
		}
		if _, err := os.Stat(daemonstate.BeingPlannedPath(r.stateDir)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the list was written (err %v)", err)
		}
		if len(r.fake.calls) != 0 {
			t.Errorf("a refused start called %v", r.fake.calls)
		}
	})

	t.Run("missing folder adds nothing", func(t *testing.T) {
		r := newBeingPlannedRig(t, nil)
		dir := filepath.Join(t.TempDir(), "gone")
		if rec := r.start(dir); rec.Code != http.StatusBadRequest {
			t.Fatalf("POST answered %d, want 400: %s", rec.Code, rec.Body)
		}
		if _, err := os.Stat(daemonstate.BeingPlannedPath(r.stateDir)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the list was written (err %v)", err)
		}
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the missing folder was created (err %v)", err)
		}
	})

	t.Run("the name is not a configured project's or a review's", func(t *testing.T) {
		other := filepath.Join(t.TempDir(), "shop")
		r := newBeingPlannedRig(t, []daemon.Project{{Name: "shop", Root: other}})
		if err := daemonstate.AddReview(r.stateDir, daemonstate.Review{Root: filepath.Join(t.TempDir(), "shop"), At: "2026-10-01T00:00:00Z"}); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(t.TempDir(), "shop")
		writePlanFile(t, dir, "keep", "")
		if rec := r.start(dir); rec.Code != http.StatusOK {
			t.Fatalf("POST answered %d: %s", rec.Code, rec.Body)
		}
		got := r.listed()
		if len(got) != 1 || got[0].Name == "shop" || got[0].Name == daemonstate.ReadReviews(r.stateDir)[0].Name {
			t.Errorf("list = %+v, want a name no configured project or review has", got)
		}
	})

	t.Run("a folder on the review list keeps the review's name", func(t *testing.T) {
		r := newBeingPlannedRig(t, nil)
		dir := filepath.Join(t.TempDir(), "shop")
		writePlanFile(t, dir, "keep", "")
		// A first review takes "shop", so this folder's review is "shop-2";
		// once the first is gone, only reusing the review's name gives
		// "shop-2" rather than the folder's own "shop".
		first := filepath.Join(t.TempDir(), "shop")
		for _, root := range []string{first, dir} {
			if err := daemonstate.AddReview(r.stateDir, daemonstate.Review{Root: root, At: "2026-10-01T00:00:00Z"}); err != nil {
				t.Fatal(err)
			}
		}
		if err := daemonstate.RemoveReview(r.stateDir, first); err != nil {
			t.Fatal(err)
		}
		review := daemonstate.ReadReviews(r.stateDir)[0].Name
		if review == "shop" {
			t.Fatalf("the review is named %q, the folder's own name; the row cannot tell reuse apart", review)
		}
		if rec := r.start(dir); rec.Code != http.StatusOK {
			t.Fatalf("POST answered %d: %s", rec.Code, rec.Body)
		}
		if got := r.listed(); len(got) != 1 || got[0].Name != review {
			t.Errorf("list = %+v, want the review's name %q", got, review)
		}
	})

	shownRows := []struct {
		name  string
		state string // "" removes state.json
		shown bool
	}{
		{"gone when state.json is removed", "", false},
		{"gone when approved", `{"mode":"new","phase":"approved","updated_at":"2026-10-12T00:00:00Z"}`, false},
		{"kept while planning", planState(plansession.PhaseRounds), true},
		{"kept at review", planState(plansession.PhaseReview), true},
		{"kept when state.json is damaged", "{not json", true},
	}
	for _, tt := range shownRows {
		t.Run(tt.name, func(t *testing.T) {
			r := newBeingPlannedRig(t, nil)
			dir := filepath.Join(t.TempDir(), "shop")
			writePlanFile(t, dir, "keep", "")
			if rec := r.start(dir); rec.Code != http.StatusOK {
				t.Fatalf("POST answered %d: %s", rec.Code, rec.Body)
			}
			state := plansession.StatePath(dir)
			if tt.state == "" {
				if err := os.Remove(state); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(state, []byte(tt.state), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := len(r.shown()) == 1; got != tt.shown {
				t.Errorf("shown = %v, want %v", r.shown(), tt.shown)
			}
			// The read only filters; the file keeps the entry until a start
			// prunes it.
			if len(r.listed()) != 1 {
				t.Errorf("the read changed the file: %+v", r.listed())
			}
		})
	}

	t.Run("the next start prunes gone entries", func(t *testing.T) {
		r := newBeingPlannedRig(t, nil)
		old := filepath.Join(t.TempDir(), "old")
		writePlanFile(t, old, "keep", "")
		if rec := r.start(old); rec.Code != http.StatusOK {
			t.Fatalf("POST answered %d: %s", rec.Code, rec.Body)
		}
		if err := os.Remove(plansession.StatePath(old)); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(t.TempDir(), "shop")
		writePlanFile(t, dir, "keep", "")
		if rec := r.start(dir); rec.Code != http.StatusOK {
			t.Fatalf("POST answered %d: %s", rec.Code, rec.Body)
		}
		got := r.listed()
		if len(got) != 1 || got[0].Root != dir {
			t.Errorf("list = %+v, want only %s", got, dir)
		}
	})
}

// TestBeingPlannedDroppedOnPlanApprove: `afk plan approve` takes the project
// off the being-planned list with the review entry, and leaves other entries
// alone. Load-bearing: the entry is gone from the file, not only hidden, and
// a second project being planned is untouched.
func TestBeingPlannedDroppedOnPlanApprove(t *testing.T) {
	r := newApproveRig(t)
	r.seed(plansession.ModeNew, plansession.PhaseReview)
	if err := daemonstate.AddReview(r.stateDir, daemonstate.Review{Root: r.root, At: "2026-10-12T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	for _, root := range []string{r.root, other} {
		if _, err := daemonstate.AddBeingPlanned(r.stateDir, daemonstate.BeingPlanned{Root: root, At: "2026-10-12T00:00:00Z"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	code, out, errOut := r.approve()
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stdout: %s stderr: %s", code, out, errOut)
	}
	got := daemonstate.ReadBeingPlanned(r.stateDir)
	if len(got) != 1 || got[0].Root != other {
		t.Errorf("being-planned list = %+v, want only %s", got, other)
	}
	if errOut != "" {
		t.Errorf("stderr = %q, want nothing", errOut)
	}
}

// TestBeingPlannedApproveListUnwritable: a list that cannot be written after
// the plan is approved is a note on stderr, never a failed approve, as for
// the review list.
func TestBeingPlannedApproveListUnwritable(t *testing.T) {
	r := newApproveRig(t)
	r.seed(plansession.ModeNew, plansession.PhaseReview)
	if err := daemonstate.AddReview(r.stateDir, daemonstate.Review{Root: r.root, At: "2026-10-12T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if _, err := daemonstate.AddBeingPlanned(r.stateDir, daemonstate.BeingPlanned{Root: r.root, At: "2026-10-12T00:00:00Z"}, nil); err != nil {
		t.Fatal(err)
	}
	// A folder where the list's temporary file would go makes the write fail
	// while the list itself still reads.
	path := daemonstate.BeingPlannedPath(r.stateDir)
	if err := os.Chmod(r.stateDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(r.stateDir, 0o700) })
	if f, err := os.CreateTemp(r.stateDir, "probe"); err == nil {
		f.Close()
		t.Skip("the state folder is still writable (running as root?)")
	}
	code, out, errOut := r.approve()
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stdout: %s stderr: %s", code, out, errOut)
	}
	if !strings.Contains(errOut, "could not be taken off the list of projects being planned ("+path+")") {
		t.Errorf("stderr does not carry the note: %q", errOut)
	}
}
