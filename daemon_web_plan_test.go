package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/planfolder"
	"github.com/dphaener/afk/internal/usagestate"
	"github.com/dphaener/afk/internal/web"
)

// TestWebApprovePlanWith: one row per outcome. Load-bearing rows: "no project"
// (it is a refusal, not a failure, because nothing was changed), "unknown"
// (an outcome this page does not know must read as failed, never as success),
// and the text check on every row (the page shows the dashboard's wording, so
// the text must be approveResultText of the same result).
func TestWebApprovePlanWith(t *testing.T) {
	tests := []struct {
		name string
		in   approveOutcome
		want string
	}{
		{"approved", approveApproved, web.PlanApproved},
		{"already", approveAlreadyApproved, web.PlanApproveAlready},
		{"refused", approveRefused, web.PlanApproveRefused},
		{"no_project", approveNoProject, web.PlanApproveRefused},
		{"failed", approveFailed, web.PlanApproveFailed},
		{"unknown", "other", web.PlanApproveFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			var gotRoot string
			res := approveResult{Outcome: tt.in, Reason: "r"}
			approve := func(root string, _ approveDeps) approveResult {
				calls++
				gotRoot = root
				return res
			}
			got := webApprovePlanWith(approve, approveDeps{}, "/proj")
			if calls != 1 || gotRoot != "/proj" {
				t.Errorf("entry point called %d times with %q, want once with /proj", calls, gotRoot)
			}
			if got.Outcome != tt.want || got.Text != approveResultText(res) {
				t.Errorf("got %+v, want outcome %q and the dashboard's text", got, tt.want)
			}
		})
	}
}

// TestWebPlanStatus runs the real file read against temp folders. Load-bearing
// rows: "new at review" (the one approvable state), "extend at review" (a
// finished extend must not offer approving), "damaged" (never approvable),
// "unreadable" (a bad afk.toml is unknown, not "no session"), and the empty,
// never nil, problems on every row (the JSON key must be a list).
func TestWebPlanStatus(t *testing.T) {
	review := func(t *testing.T, root, mode string) {
		writePlanFile(t, root, ".afk/plan/state.json", `{"mode":"`+mode+`","phase":"review","updated_at":"2026-10-12T00:00:00Z"}`)
		for _, f := range []string{"brief.md", "survey.md", "slices.md", "critic.md"} {
			writePlanFile(t, root, ".afk/plan/"+f, "text\n")
		}
		writePlanFile(t, root, ".afk/plan/questions.jsonl", planQuestions("no"))
		writePlanFile(t, root, "docs/PLAN.md", dashPlanCleanText)
	}
	tests := []struct {
		name           string
		setup          func(t *testing.T, root string)
		known, session bool
		damaged        bool
		approvable     bool
		phase          string
	}{
		{"no session", func(t *testing.T, root string) {}, true, false, false, false, ""},
		{"damaged", func(t *testing.T, root string) {
			writePlanFile(t, root, ".afk/plan/state.json", "{not json")
		}, true, true, true, false, ""},
		{"new at review", func(t *testing.T, root string) { review(t, root, "new") }, true, true, false, true, "review"},
		{"new at check", func(t *testing.T, root string) {
			writePlanFile(t, root, ".afk/plan/state.json", planState("check"))
		}, true, true, false, false, ""},
		{"extend at review", func(t *testing.T, root string) { review(t, root, "extend") }, true, true, false, false, ""},
		{"approved", func(t *testing.T, root string) {
			writePlanFile(t, root, ".afk/plan/state.json", planState("approved"))
		}, true, true, false, false, ""},
		{"unreadable", func(t *testing.T, root string) {
			writePlanFile(t, root, "afk.toml", "plan = [")
		}, false, false, false, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			tt.setup(t, root)
			got := webPlanStatus(root)
			if got.Known != tt.known || got.Session != tt.session || got.Damaged != tt.damaged || got.Approvable != tt.approvable {
				t.Errorf("got %+v, want known=%v session=%v damaged=%v approvable=%v", got, tt.known, tt.session, tt.damaged, tt.approvable)
			}
			if tt.phase != "" && got.Phase != tt.phase {
				t.Errorf("phase = %q, want %q", got.Phase, tt.phase)
			}
			if got.Problems == nil {
				t.Error("problems is nil, want an empty list")
			}
			if got.Known && tt.session && got.Line == "" {
				t.Error("a known session has an empty plan line")
			}
		})
	}
}

// TestWebPlanStatusRunsNoGit: the lookup webPlanStatus hands the loader
// answers "not in a repository", so the root is used as given and a page
// request runs no git. The spy in webPlanStatusWith proves the mapping works
// with any parts; here the real parts' lookup is checked directly.
func TestWebPlanStatusRunsNoGit(t *testing.T) {
	root := t.TempDir()
	calls := 0
	parts := dashPlanStatusParts{gitCommonDir: func(dir string) string { calls++; return "" }}
	got := webPlanStatusWith(parts, root)
	if !got.Known || got.Session {
		t.Errorf("got %+v, want known with no session", got)
	}
	if want := (web.PlanStatus{Known: true, Problems: []string{}, Line: "no planning session"}); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// TestDaemonWebPlanSeams: the daemon's deps offer every plan seam. The page
// hides Plan a new project when any of its three seams is nil, so a missing
// one would read as a feature that was never built.
func TestDaemonWebPlanSeams(t *testing.T) {
	d := daemonWebDeps(nil, t.TempDir(), usagestate.Deps{}, nil)
	if d.PlanStatus == nil || d.ApprovePlan == nil || d.StartPlan == nil ||
		d.CheckPlanFolder == nil || d.CreatePlanFolder == nil || d.PlanLocation == nil ||
		d.AddBeingPlanned == nil || d.BeingPlanned == nil {
		t.Fatalf("a plan seam is nil: %+v", d)
	}
}

// TestWebStartPlanWith: one row per outcome. Load-bearing rows: "unknown
// outcome" (must read as refused, never started), "wait fails on started"
// (the owner is sent to a chat whose host record is missing, so it must be a
// refusal with the shared sentence), "refused does not wait" (a refusal has no
// host to wait for), and the request check on every row (mode, root and text
// pass through exactly, and the provider stays empty).
func TestWebStartPlanWith(t *testing.T) {
	waitErr := errors.New("no host.json")
	tests := []struct {
		name     string
		res      planSurfaceResult
		waitErr  error
		wantWait bool
		want     web.PlanStartResult
	}{
		{"started", planSurfaceResult{Outcome: planSurfaceStarted}, nil, true,
			web.PlanStartResult{Outcome: web.PlanStartStarted}},
		{"resumed", planSurfaceResult{Outcome: planSurfaceResumed}, nil, true,
			web.PlanStartResult{Outcome: web.PlanStartResumed}},
		{"refused", planSurfaceResult{Outcome: planSurfaceRefused, Reason: "nope"}, nil, false,
			web.PlanStartResult{Outcome: web.PlanStartRefused, Reason: "nope"}},
		{"session open", planSurfaceResult{Outcome: planSurfaceRefused, Reason: "open", SessionOpen: true, OpenMode: "extend", OpenPhase: "interview"}, nil, false,
			web.PlanStartResult{Outcome: web.PlanStartRefused, Reason: "open", Open: true, OpenMode: "extend", OpenPhase: "interview"}},
		{"unknown outcome", planSurfaceResult{Outcome: "later"}, nil, false,
			web.PlanStartResult{Outcome: web.PlanStartRefused}},
		{"wait fails on started", planSurfaceResult{Outcome: planSurfaceStarted}, waitErr, true,
			web.PlanStartResult{Outcome: web.PlanStartRefused, Reason: planHostNotReadyReason(waitErr)}},
		{"wait fails on resumed", planSurfaceResult{Outcome: planSurfaceResumed}, waitErr, true,
			web.PlanStartResult{Outcome: web.PlanStartRefused, Reason: planHostNotReadyReason(waitErr)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotReq planSurfaceRequest
			start := func(r planSurfaceRequest, _ planDeps) planSurfaceResult {
				gotReq = r
				return tt.res
			}
			var waited []string
			wait := func(root string) error {
				waited = append(waited, root)
				return tt.waitErr
			}
			got := webStartPlanWith(start, planDeps{}, wait, "/proj", "amend", "say it")
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("result = %+v, want %+v", got, tt.want)
			}
			if gotReq != (planSurfaceRequest{Mode: "amend", Project: "/proj", Text: "say it"}) {
				t.Errorf("request = %+v", gotReq)
			}
			if tt.wantWait != (len(waited) == 1) || (tt.wantWait && waited[0] != "/proj") {
				t.Errorf("waited = %v, want wait %v", waited, tt.wantWait)
			}
		})
	}
}

// TestWebStartPlanWithRoot pins the folder the page is told planning runs in.
//
// Load-bearing rows:
//   - "new through a link is resolved": the being-planned list and the page's
//     project name are keyed by this folder, and the owner may have typed a
//     path through a link. The dashboard resolves it the same way.
//   - "amend keeps the root as given": a configured project's folder is the
//     name the rest of the page knows it by, so it must not be rewritten.
//   - "new from a subfolder waits in the main checkout": host.json is written
//     where the host runs, so waiting in the typed folder would never see it.
//   - "refused carries the root, no wait": a refusal has no host to wait for.
func TestWebStartPlanWithRoot(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		mode     string
		sent     string
		res      planSurfaceResult
		wantRoot string
		wantWait []string
	}{
		{"new through a link is resolved", planSurfaceNew, link,
			planSurfaceResult{Outcome: planSurfaceStarted, Root: link}, real, []string{real}},
		{"new, root that cannot be resolved is kept", planSurfaceNew, "/gone/shop",
			planSurfaceResult{Outcome: planSurfaceResumed, Root: "/gone/shop"}, "/gone/shop", []string{"/gone/shop"}},
		{"amend keeps the root as given", "amend", link,
			planSurfaceResult{Outcome: planSurfaceStarted, Root: link}, link, []string{link}},
		{"new from a subfolder waits in the main checkout", planSurfaceNew, filepath.Join(real, "sub"),
			planSurfaceResult{Outcome: planSurfaceStarted, Root: real}, real, []string{real}},
		{"no root waits in the folder sent", planSurfaceNew, real,
			planSurfaceResult{Outcome: planSurfaceStarted}, "", []string{real}},
		{"refused carries the root, no wait", planSurfaceNew, real,
			planSurfaceResult{Outcome: planSurfaceRefused, Reason: "no", Root: real}, real, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := func(planSurfaceRequest, planDeps) planSurfaceResult { return tt.res }
			var waited []string
			wait := func(root string) error {
				waited = append(waited, root)
				return nil
			}
			got := webStartPlanWith(start, planDeps{}, wait, tt.sent, tt.mode, "")
			if got.Root != tt.wantRoot {
				t.Errorf("root = %q, want %q", got.Root, tt.wantRoot)
			}
			if !reflect.DeepEqual(waited, tt.wantWait) {
				t.Errorf("waited in %v, want %v", waited, tt.wantWait)
			}
		})
	}
}

// TestDaemonWebPlanSeamsLocation pins webPlanLocationFrom, the rule behind
// the Where field's hint. Load-bearing rows: "set but a problem" and "not
// set" must be "", or the hint would promise new folders in a place a name
// cannot be made.
func TestDaemonWebPlanSeamsLocation(t *testing.T) {
	tests := []struct {
		name string
		loc  planfolder.Location
		want string
	}{
		{"set", planfolder.Location{Path: "/home/me/code", Set: true}, "/home/me/code"},
		{"not set", planfolder.Location{}, ""},
		{"set but a problem", planfolder.Location{Set: true, Problem: "not a full path"}, ""},
		{"profile unreadable", planfolder.Location{Problem: "could not be read"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := webPlanLocationFrom(tt.loc); got != tt.want {
				t.Errorf("webPlanLocationFrom(%+v) = %q, want %q", tt.loc, got, tt.want)
			}
		})
	}
}

// TestWebCheckPlanFolderNames pins what the page's Where field means for a
// bare name, against a fake profile location and real temp folders.
//
// Load-bearing rows:
//   - "new name": kind create with the full path under the location and the
//     typed name, and nothing made: the GET draws the card from this.
//   - "existing empty repo by name" and "existing plain folder by name": the
//     folder's own kind (never refused with the dashboard's sentence), with
//     Name set so the card can say it was found, not made.
//   - "bad characters", "no location", "location missing", "location
//     problem" and "a file of that name": refused in planfolder's words.
//   - "a path" rows: a text with / or ~ goes to the T8.15 check unchanged,
//     with no Name, and the missing path gets the page's sentence with no
//     --project.
func TestWebCheckPlanFolderNames(t *testing.T) {
	tests := []struct {
		name string
		// text may hold {loc}, the location folder.
		text string
		// loc: "set" (the temp folder), "none", "missing" or "problem".
		loc   string
		setup func(t *testing.T, loc string)
		// repo makes the fake git answer that every folder is a repository.
		repo       bool
		want       string
		wantPath   string // may hold {loc}; "" means no path
		wantName   string
		wantReason string
	}{
		{name: "new name", text: "demo", loc: "set",
			want: web.PlanFolderCreate, wantPath: "{loc}/demo", wantName: "demo"},
		{name: "new name, spaces trimmed", text: "  demo  ", loc: "set",
			want: web.PlanFolderCreate, wantPath: "{loc}/demo", wantName: "demo"},
		{name: "bad characters", text: "bad name!", loc: "set",
			want: web.PlanFolderRefused, wantName: "bad name!", wantReason: "Use letters, digits, `.`, `_` and `-`"},
		{name: "no location", text: "demo", loc: "none",
			want: web.PlanFolderRefused, wantName: "demo", wantReason: "afk profile set \"project location\" ~/code"},
		{name: "location missing", text: "demo", loc: "missing",
			want: web.PlanFolderRefused, wantName: "demo", wantReason: "does not exist. Create it first"},
		{name: "location problem", text: "demo", loc: "problem",
			want: web.PlanFolderRefused, wantName: "demo", wantReason: "is not a full path"},
		{name: "existing empty repo by name", text: "shop", loc: "set", repo: true,
			setup: func(t *testing.T, loc string) { writePlanFile(t, filepath.Join(loc, "shop"), "keep", "") },
			want:  web.PlanFolderRepo, wantPath: "{loc}/shop", wantName: "shop"},
		{name: "existing plain folder by name", text: "shop", loc: "set",
			setup: func(t *testing.T, loc string) { writePlanFile(t, filepath.Join(loc, "shop"), "keep", "") },
			want:  web.PlanFolderFolder, wantPath: "{loc}/shop", wantName: "shop"},
		{name: "existing folder with a plan by name", text: "shop", loc: "set", repo: true,
			setup: func(t *testing.T, loc string) {
				writePlanFile(t, filepath.Join(loc, "shop"), "docs/PLAN.md", "# Plan\n")
			},
			want: web.PlanFolderHasPlan, wantPath: "{loc}/shop", wantName: "shop"},
		{name: "a file of that name", text: "notes", loc: "set",
			setup: func(t *testing.T, loc string) { writePlanFile(t, loc, "notes", "x\n") },
			want:  web.PlanFolderRefused, wantName: "notes", wantReason: "already exists and is not a folder"},
		{name: "a path to a repo", text: "{loc}/shop", loc: "set", repo: true,
			setup: func(t *testing.T, loc string) { writePlanFile(t, filepath.Join(loc, "shop"), "keep", "") },
			want:  web.PlanFolderRepo, wantPath: "{loc}/shop"},
		{name: "a missing path", text: "{loc}/gone", loc: "set",
			want: web.PlanFolderRefused, wantPath: "{loc}/gone",
			wantReason: "There is no folder at {loc}/gone. Type the path of a folder that exists, or a plain name to make a new folder in your project location. Nothing was started."},
		{name: "a path to a file", text: "{loc}/notes", loc: "set",
			setup: func(t *testing.T, loc string) { writePlanFile(t, loc, "notes", "x\n") },
			want:  web.PlanFolderRefused, wantPath: "{loc}/notes",
			wantReason: "{loc}/notes is a file, not a folder. Type the path of a folder. Nothing was started."},
		{name: "a relative path is not a name", text: "code/shop", loc: "set",
			want: web.PlanFolderRefused, wantPath: "", wantReason: "is not a full path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loc := t.TempDir()
			if tt.setup != nil {
				tt.setup(t, loc)
			}
			var location planfolder.Location
			switch tt.loc {
			case "set":
				location = planfolder.Location{Path: loc, Set: true}
			case "missing":
				location = planfolder.Location{Path: filepath.Join(loc, "nowhere"), Set: true}
			case "problem":
				location = planfolder.Location{Set: true, Problem: `Your project location "code" is not a full path.`}
			}
			f := &planFake{home: t.TempDir()}
			if tt.repo {
				f.commonDir = filepath.Join(loc, "shop", ".git")
			}
			before := folderFiles(t, loc)
			sub := func(s string) string { return strings.ReplaceAll(s, "{loc}", loc) }

			got := webCheckPlanFolderNamesWith(func() planfolder.Location { return location }, os.Stat, planCheckDeps(f.deps(t.TempDir())), sub(tt.text))

			if got.Kind != tt.want {
				t.Fatalf("kind = %q, want %q; reason: %s", got.Kind, tt.want, got.Reason)
			}
			if want := sub(tt.wantPath); got.Path != want {
				t.Errorf("path = %q, want %q", got.Path, want)
			}
			if got.Name != tt.wantName {
				t.Errorf("name = %q, want %q", got.Name, tt.wantName)
			}
			if !strings.Contains(got.Reason, sub(tt.wantReason)) {
				t.Errorf("reason missing %q:\n%s", sub(tt.wantReason), got.Reason)
			}
			if (got.Kind == web.PlanFolderRefused) != (got.Reason != "") {
				t.Errorf("reason %q on kind %q: only a refusal has one", got.Reason, got.Kind)
			}
			for _, never := range []string{"--project", "The dashboard can plan in it"} {
				if strings.Contains(got.Reason, never) {
					t.Errorf("reason has %q, which is not the page's: %s", never, got.Reason)
				}
			}
			if len(f.calls) != 0 {
				t.Errorf("the check called %v, want nothing", f.calls)
			}
			if after := folderFiles(t, loc); !reflect.DeepEqual(after, before) {
				t.Errorf("the check changed the location:\nbefore %v\nafter  %v", before, after)
			}
		})
	}
}

// TestWebCreatePlanFolder pins the create step behind the page's confirmed
// POST, with a fake profile location, a recording mkdir that really makes the
// folder, and a fake git init.
//
// Load-bearing rows:
//   - "a full path", "a ~ path" and "a path out of the location": the guard.
//     The text comes from a POST, so anything but a bare name whose folder is
//     missing under the location must make nothing (mkdir never called).
//   - "existing name": the folder appeared after the card; nothing is made.
//   - "git init fails": the folder is left on disk, its path returned, the
//     sentence says so and that nothing was removed.
//   - "mkdir fails": nothing was made, so no path is returned and git init is
//     never run.
func TestWebCreatePlanFolder(t *testing.T) {
	initErr := errors.New("git: not found")
	tests := []struct {
		name     string
		text     string // may hold {loc}
		noLoc    bool
		setup    func(t *testing.T, loc string)
		mkdirErr error
		initErr  error
		// wantPath may hold {loc}; "" means none.
		wantPath  string
		wantErr   []string // each may hold {loc}; nil means no error
		wantMkdir bool
		wantInit  bool
		// wantDir is whether {loc}/demo is on disk afterwards.
		wantDir bool
	}{
		{name: "new name", text: "demo", wantPath: "{loc}/demo", wantMkdir: true, wantInit: true, wantDir: true},
		{name: "new name, spaces trimmed", text: " demo ", wantPath: "{loc}/demo", wantMkdir: true, wantInit: true, wantDir: true},
		{name: "a full path", text: "{loc}/demo", wantErr: []string{"only a plain project name makes a new folder", "Nothing was created."}},
		{name: "a ~ path", text: "~/demo", wantErr: []string{"is a path", "Nothing was created."}},
		{name: "a path out of the location", text: "../demo", wantErr: []string{"is a path"}},
		{name: "existing name", text: "demo", wantDir: true,
			setup:   func(t *testing.T, loc string) { writePlanFile(t, filepath.Join(loc, "demo"), "keep", "") },
			wantErr: []string{"{loc}/demo already exists, so nothing was created."}},
		{name: "bad characters", text: "bad name!", wantErr: []string{"is not a usable project name"}},
		{name: "no location", text: "demo", noLoc: true, wantErr: []string{"No project location is set"}},
		{name: "git init fails", text: "demo", initErr: initErr, wantPath: "{loc}/demo", wantMkdir: true, wantInit: true, wantDir: true,
			wantErr: []string{"was made and is left on disk", "Nothing was removed.", initErr.Error()}},
		{name: "mkdir fails", text: "demo", mkdirErr: os.ErrPermission, wantMkdir: true,
			wantErr: []string{"could not be made", "Nothing was created."}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loc := t.TempDir()
			if tt.setup != nil {
				tt.setup(t, loc)
			}
			sub := func(s string) string { return strings.ReplaceAll(s, "{loc}", loc) }
			location := planfolder.Location{Path: loc, Set: true}
			if tt.noLoc {
				location = planfolder.Location{}
			}
			var mkdirs, inits []string
			mkdir := func(p string) error {
				mkdirs = append(mkdirs, p)
				if tt.mkdirErr != nil {
					return tt.mkdirErr
				}
				// Only ever inside the temp location, so a broken guard
				// shows as a failed row and never leaves a folder in the
				// test's working directory.
				if filepath.Dir(p) != loc {
					return fmt.Errorf("the fake mkdir refuses %s, outside the location", p)
				}
				return os.Mkdir(p, 0o755)
			}
			gitInit := func(p string) error {
				inits = append(inits, p)
				return tt.initErr
			}

			path, err := webCreatePlanFolderWith(func() planfolder.Location { return location }, os.Stat, mkdir, gitInit, sub(tt.text))

			if path != sub(tt.wantPath) {
				t.Errorf("path = %q, want %q", path, sub(tt.wantPath))
			}
			if (err != nil) != (tt.wantErr != nil) {
				t.Fatalf("err = %v, want an error %v", err, tt.wantErr != nil)
			}
			for _, w := range tt.wantErr {
				if !strings.Contains(err.Error(), sub(w)) {
					t.Errorf("error missing %q:\n%v", sub(w), err)
				}
			}
			if want := fmt.Sprint(tt.wantMkdir); fmt.Sprint(len(mkdirs) == 1) != want || (tt.wantMkdir && mkdirs[0] != filepath.Join(loc, "demo")) {
				t.Errorf("mkdir calls = %v, want one for %s: %v", mkdirs, filepath.Join(loc, "demo"), tt.wantMkdir)
			}
			if (len(inits) == 1) != tt.wantInit || len(inits) > 1 {
				t.Errorf("git init calls = %v, want one: %v", inits, tt.wantInit)
			}
			_, statErr := os.Stat(filepath.Join(loc, "demo"))
			if (statErr == nil) != tt.wantDir {
				t.Errorf("%s on disk = %v, want %v", filepath.Join(loc, "demo"), statErr == nil, tt.wantDir)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(loc), "demo")); err == nil {
				t.Errorf("a folder was made outside the location")
			}
		})
	}
}
