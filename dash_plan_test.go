package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/planfolder"
	"github.com/dphaener/afk/internal/ui"
)

// TestDashPlanAdapter pins how the entry point's result becomes the
// dashboard's. Load-bearing rows: "refused" (reason carried, no open flag, so
// no offer to open) and "unknown outcome" (falls to refused, so no pane opens
// by accident); "refused because open" keeps the mode and phase for the offer.
func TestDashPlanAdapter(t *testing.T) {
	tests := []struct {
		name string
		res  planSurfaceResult
		want ui.PlanStartResult
	}{
		{"started", planSurfaceResult{Outcome: planSurfaceStarted, Root: "/p", Archived: "/p/.afk/old"},
			ui.PlanStartResult{Outcome: ui.PlanStarted, Root: "/p", Archive: "/p/.afk/old"}},
		{"resumed", planSurfaceResult{Outcome: planSurfaceResumed, Root: "/p"},
			ui.PlanStartResult{Outcome: ui.PlanResumed, Root: "/p"}},
		{"refused", planSurfaceResult{Outcome: planSurfaceRefused, Reason: "no plan here.", Root: "/p"},
			ui.PlanStartResult{Outcome: ui.PlanRefused, Reason: "no plan here.", Root: "/p"}},
		{"refused because open", planSurfaceResult{Outcome: planSurfaceRefused, Reason: "open", SessionOpen: true, OpenMode: "extend", OpenPhase: "rounds", Root: "/p"},
			ui.PlanStartResult{Outcome: ui.PlanRefused, Reason: "open", Open: true, OpenMode: "extend", OpenPhase: "rounds", Root: "/p"}},
		{"unknown outcome", planSurfaceResult{Outcome: "other"},
			ui.PlanStartResult{Outcome: ui.PlanRefused}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got planSurfaceRequest
			start := func(req planSurfaceRequest, _ planDeps) planSurfaceResult {
				got = req
				return tt.res
			}
			out := dashStartPlanWith(start, planDeps{}, "/proj", "amend", "say it")
			if out != tt.want {
				t.Errorf("result = %+v, want %+v", out, tt.want)
			}
			if got.Mode != "amend" || got.Project != "/proj" || got.Text != "say it" {
				t.Errorf("request = %+v, want mode amend, project /proj, text 'say it'", got)
			}
		})
	}
}

// TestDashStartPlanMode pins that the adapter hands the mode and the text to
// the entry point unchanged, so "fix a bug" reaches startPlanForSurface as
// mode fix with the owner's sentence. Load-bearing rows: "fix" (the new mode
// must arrive as fix, not be mapped to amend or refused) and "amend" (the
// existing mode the adapter already passed through, which the new action must
// not change). The name starts with TestDashStartPlan so that -run pattern
// selects it.
func TestDashStartPlanMode(t *testing.T) {
	tests := []struct {
		name string
		mode string
		text string
	}{
		{"fix", "fix", "the export button does nothing"},
		{"amend", "amend", "say it"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got planSurfaceRequest
			start := func(req planSurfaceRequest, _ planDeps) planSurfaceResult {
				got = req
				return planSurfaceResult{}
			}
			dashStartPlanWith(start, planDeps{}, "/proj", tt.mode, tt.text)
			if got.Mode != tt.mode || got.Project != "/proj" || got.Text != tt.text {
				t.Errorf("request = %+v, want mode %q, project /proj, text %q", got, tt.mode, tt.text)
			}
		})
	}
}

// TestDashPlanDepsSetsSeam checks dashDeps wires StartPlan; left nil, the
// three actions would say the feature is not available.
func TestDashPlanDepsSetsSeam(t *testing.T) {
	if dashDeps("", dashLocation{}, nil, -1).StartPlan == nil {
		t.Fatal("dashDeps(...).StartPlan is nil, want dashStartPlan")
	}
}

// TestWaitPlanHostReady pins when the dashboard waits for host.json. The
// started and resumed rows are load-bearing: the pane opens right after, so
// they must wait. A refusal must not wait (nothing was started), and a wait
// that fails must become a refusal so no pane is opened on a missing host.
func TestWaitPlanHostReady(t *testing.T) {
	tests := []struct {
		name      string
		in        ui.PlanStartResult
		waitErr   error
		wantWaits int
		want      ui.PlanStartOutcome
	}{
		{"started waits", ui.PlanStartResult{Outcome: ui.PlanStarted, Root: "/p"}, nil, 1, ui.PlanStarted},
		{"resumed waits", ui.PlanStartResult{Outcome: ui.PlanResumed, Root: "/p"}, nil, 1, ui.PlanResumed},
		{"refused does not wait", ui.PlanStartResult{Outcome: ui.PlanRefused, Root: "/p"}, nil, 0, ui.PlanRefused},
		{"wait fails", ui.PlanStartResult{Outcome: ui.PlanStarted, Root: "/p"}, errors.New("no host.json"), 1, ui.PlanRefused},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			waits := 0
			out := waitPlanHostReady(tt.in, func(root string) error {
				waits++
				if root != "/p" {
					t.Errorf("waited on %q, want /p", root)
				}
				return tt.waitErr
			})
			if waits != tt.wantWaits || out.Outcome != tt.want {
				t.Errorf("waits=%d outcome=%v, want waits=%d outcome=%v", waits, out.Outcome, tt.wantWaits, tt.want)
			}
			if tt.waitErr != nil && out.Reason == "" {
				t.Error("failed wait gave no reason")
			}
		})
	}
}

// TestDashPlanNewRootResolved pins that the new mode's root is symlink
// resolved (the switcher compares resolved paths) and that the other modes'
// roots are passed through untouched. The "new" row is load-bearing: a link in
// the typed folder would otherwise add a second switcher row for the project.
func TestDashPlanNewRootResolved(t *testing.T) {
	real := t.TempDir()
	if r, err := filepath.EvalSymlinks(real); err == nil {
		real = r
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, mode, root, want string
	}{
		{"new resolves a link", planSurfaceNew, link, real},
		{"new keeps a root that cannot be resolved", planSurfaceNew, filepath.Join(real, "gone"), filepath.Join(real, "gone")},
		{"new keeps an empty root", planSurfaceNew, "", ""},
		{"extend keeps the root as it is", "extend", link, link},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := func(planSurfaceRequest, planDeps) planSurfaceResult {
				return planSurfaceResult{Outcome: planSurfaceStarted, Root: tt.root}
			}
			if got := dashStartPlanWith(start, planDeps{}, "/x", tt.mode, "").Root; got != tt.want {
				t.Errorf("root = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestDashPlanFolderMapping pins how the decision becomes the dashboard's.
// Load-bearing rows: "refused" (the zero kind, so a refusal never creates or
// starts anything) and "exists" (the flag and the path survive, or the offer to
// plan in the folder is lost).
func TestDashPlanFolderMapping(t *testing.T) {
	loc := planfolder.Location{Path: "/loc", Set: true}
	dir := t.TempDir()
	tests := []struct {
		name string
		loc  planfolder.Location
		text string
		want ui.PlanFolderDecision
	}{
		{"create", planfolder.Location{Path: dir, Set: true}, "demo",
			ui.PlanFolderDecision{Kind: ui.PlanFolderCreate, Path: filepath.Join(dir, "demo")}},
		{"use", loc, "/some/where",
			ui.PlanFolderDecision{Kind: ui.PlanFolderUse, Path: "/some/where"}},
		{"refused", planfolder.Location{}, "my app",
			ui.PlanFolderDecision{}},
	}
	if err := os.Mkdir(filepath.Join(dir, "there"), 0o755); err != nil {
		t.Fatal(err)
	}
	tests = append(tests, struct {
		name string
		loc  planfolder.Location
		text string
		want ui.PlanFolderDecision
	}{"exists", planfolder.Location{Path: dir, Set: true}, "there",
		ui.PlanFolderDecision{Path: filepath.Join(dir, "there"), Exists: true}})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dashPlanFolderWith(func() planfolder.Location { return tt.loc }, os.Stat, tt.text)
			reason := got.Reason
			got.Reason = ""
			if tt.want.Kind == ui.PlanFolderRefused && reason == "" {
				t.Errorf("refusal %+v has no reason", got)
			}
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestDashPlanFolderDepsSetsSeams checks dashDeps wires both folder seams;
// left nil, a typed name would be handed to the start as a folder path.
func TestDashPlanFolderDepsSetsSeams(t *testing.T) {
	d := dashDeps("", dashLocation{}, nil, -1)
	if d.PlanFolder == nil || d.CreatePlanFolder == nil {
		t.Fatalf("PlanFolder nil: %v, CreatePlanFolder nil: %v", d.PlanFolder == nil, d.CreatePlanFolder == nil)
	}
}

// TestDashPlanCreateFolder makes a real folder in a temp location and checks
// the location itself is never created.
func TestDashPlanCreateFolder(t *testing.T) {
	loc := t.TempDir()
	create := func(p string) error {
		return planfolder.Create(p, func(p string) error { return os.Mkdir(p, 0o755) }, gitInitIn)
	}
	noTrust := func(string) error { return nil }
	if err := dashCreatePlanFolderWith(create, noTrust, filepath.Join(loc, "demo")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(loc, "demo", ".git")); err != nil {
		t.Errorf(".git missing: %v", err)
	}
	if err := dashCreatePlanFolderWith(create, noTrust, filepath.Join(loc, "missing", "demo")); err == nil {
		t.Error("creating under a missing location succeeded, want an error")
	}
	if _, err := os.Stat(filepath.Join(loc, "missing")); err == nil {
		t.Error("the location folder was created")
	}
}

// TestDashCreatePlanFolderTrust pins when the new folder is marked trusted.
//
// Load-bearing rows:
//   - "create ok": trust runs once with the folder path.
//   - "create fails": trust never runs, and the create error comes back.
//   - "trust fails": nil comes back, because the folder exists and planning
//     must start anyway.
func TestDashCreatePlanFolderTrust(t *testing.T) {
	createErr := errors.New("git: not found")
	trustErr := errors.New("no settings file")
	tests := []struct {
		name      string
		createErr error
		trustErr  error
		wantErr   error
		wantTrust int
	}{
		{name: "create ok", wantTrust: 1},
		{name: "create fails", createErr: createErr, wantErr: createErr},
		{name: "trust fails", trustErr: trustErr, wantTrust: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var trusted []string
			err := dashCreatePlanFolderWith(
				func(string) error { return tt.createErr },
				func(p string) error { trusted = append(trusted, p); return tt.trustErr },
				"/loc/demo")
			if err != tt.wantErr {
				t.Errorf("err = %v, want %v", err, tt.wantErr)
			}
			if len(trusted) != tt.wantTrust || (len(trusted) == 1 && trusted[0] != "/loc/demo") {
				t.Errorf("trust calls = %v, want %d for /loc/demo", trusted, tt.wantTrust)
			}
		})
	}
}

// fakeDashGH is a recording ghrepo.GH: it never runs gh, and it counts the
// calls so a test can see how far the offer went and that yes creates once.
type fakeDashGH struct {
	authErr    error
	login      string
	loginErr   error
	createdURL string
	createErr  error

	authCalls, loginCalls int
	creates               []string
}

func (f *fakeDashGH) AuthStatus() error { f.authCalls++; return f.authErr }
func (f *fakeDashGH) Login() (string, error) {
	f.loginCalls++
	return f.login, f.loginErr
}
func (f *fakeDashGH) Protocol() string { return "https" }
func (f *fakeDashGH) CreatePrivate(repo string) (string, error) {
	f.creates = append(f.creates, repo)
	return f.createdURL, f.createErr
}

// fakeDashGit is a recording ghrepo.Git.
type fakeDashGit struct {
	err  error
	adds [][2]string
}

func (f *fakeDashGit) AddOrigin(dir, url string) error {
	f.adds = append(f.adds, [2]string{dir, url})
	return f.err
}

// TestDashGitHubOfferMapping pins how the checks become the dashboard's offer.
// Load-bearing rows: "gh missing" (gh is never run, so a missing gh cannot
// be mistaken for a logged-out one) and "logged out" (the account is never
// asked for, and the notice names `gh auth login`); "ask" carries the
// account/folder name the question shows.
func TestDashGitHubOfferMapping(t *testing.T) {
	tests := []struct {
		name       string
		lookErr    error
		gh         *fakeDashGH
		want       ui.GitHubOffer
		wantNotice string
		wantAuth   int
		wantLogin  int
	}{
		{"ask", nil, &fakeDashGH{login: "owner"},
			ui.GitHubOffer{Ask: true, Repo: "owner/demo"}, "", 1, 1},
		{"gh missing", exec.ErrNotFound, &fakeDashGH{login: "owner"},
			ui.GitHubOffer{}, "not installed", 0, 0},
		{"logged out", nil, &fakeDashGH{authErr: errors.New("exit status 1"), login: "owner"},
			ui.GitHubOffer{}, "gh auth login", 1, 0},
		{"no account", nil, &fakeDashGH{loginErr: errors.New("HTTP 401")},
			ui.GitHubOffer{}, "HTTP 401", 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			look := func(name string) (string, error) {
				if name != "gh" {
					t.Errorf("looked up %q, want gh", name)
				}
				return "/bin/gh", tt.lookErr
			}
			got := dashGitHubOfferWith(look, tt.gh, "/loc/demo")
			notice := got.Notice
			got.Notice = ""
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
			if tt.wantNotice == "" && notice != "" {
				t.Errorf("notice = %q, want none", notice)
			}
			if !strings.Contains(notice, tt.wantNotice) {
				t.Errorf("notice = %q, want it to contain %q", notice, tt.wantNotice)
			}
			if tt.gh.authCalls != tt.wantAuth || tt.gh.loginCalls != tt.wantLogin {
				t.Errorf("auth calls %d, login calls %d; want %d, %d", tt.gh.authCalls, tt.gh.loginCalls, tt.wantAuth, tt.wantLogin)
			}
			if len(tt.gh.creates) != 0 {
				t.Errorf("the offer created %v", tt.gh.creates)
			}
		})
	}
}

// TestDashCreateGitHubRepo pins the create seam. Load-bearing rows: "yes"
// (exactly one private create and one origin add with the .git URL) and
// "name taken" (no origin is added to a repository that is not ours).
func TestDashCreateGitHubRepo(t *testing.T) {
	tests := []struct {
		name     string
		gh       *fakeDashGH
		git      *fakeDashGit
		wantAdds [][2]string
		wantLine string
	}{
		{"yes", &fakeDashGH{createdURL: "https://github.com/owner/demo\n"}, &fakeDashGit{},
			[][2]string{{"/loc/demo", "https://github.com/owner/demo.git"}}, "Made the private GitHub repository owner/demo"},
		{"name taken", &fakeDashGH{createErr: errors.New("GraphQL: Name already exists on this account")}, &fakeDashGit{},
			nil, "already has a repository named owner/demo"},
		{"origin fails", &fakeDashGH{createdURL: "https://github.com/owner/demo"}, &fakeDashGit{err: errors.New("remote origin already exists")},
			[][2]string{{"/loc/demo", "https://github.com/owner/demo.git"}}, "could not set it as origin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line := dashCreateGitHubRepoWith(tt.gh, tt.git, "/loc/demo", "owner/demo")
			if len(tt.gh.creates) != 1 || tt.gh.creates[0] != "owner/demo" {
				t.Errorf("creates = %v, want exactly [owner/demo]", tt.gh.creates)
			}
			if len(tt.git.adds) != len(tt.wantAdds) {
				t.Fatalf("origin adds = %v, want %v", tt.git.adds, tt.wantAdds)
			}
			for i := range tt.wantAdds {
				if tt.git.adds[i] != tt.wantAdds[i] {
					t.Errorf("origin add %d = %v, want %v", i, tt.git.adds[i], tt.wantAdds[i])
				}
			}
			if !strings.Contains(line, tt.wantLine) {
				t.Errorf("line = %q, want it to contain %q", line, tt.wantLine)
			}
		})
	}
}

// TestDashGitHubDepsSetsSeams checks dashDeps wires both GitHub seams; left
// nil, internal/ui keeps T7.5's behaviour and the question is never asked.
func TestDashGitHubDepsSetsSeams(t *testing.T) {
	d := dashDeps("", dashLocation{}, nil, -1)
	if d.GitHubOffer == nil || d.CreateGitHubRepo == nil {
		t.Fatalf("GitHubOffer nil: %v, CreateGitHubRepo nil: %v", d.GitHubOffer == nil, d.CreateGitHubRepo == nil)
	}
}
