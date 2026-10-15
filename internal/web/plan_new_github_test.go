package web

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// ghFake stands in for the root's gh seams: it counts every call and makes
// nothing anywhere.
type ghFake struct {
	offer       GitHubOffer
	offerCalls  []string
	outcome     GitHubOutcome
	createCalls [][2]string
	now         time.Time
}

const ghMade = "Made the private GitHub repository me/demo and set it as origin. Nothing was pushed."

// ghRig is createRig with the GitHub seams set: gh is ready for account me,
// a yes makes me/demo, and the clock is the fake's.
func ghRig(t *testing.T) (*rig, *ghFake) {
	t.Helper()
	rg := createRig(t)
	f := &ghFake{
		offer:   GitHubOffer{Ask: true, Repo: "me/demo", Reason: "ask"},
		outcome: GitHubOutcome{Kind: GitHubCreated, Repo: "me/demo", Text: ghMade},
		now:     testNow,
	}
	rg.srv.d.GitHubOffer = func(path string) GitHubOffer {
		f.offerCalls = append(f.offerCalls, path)
		return f.offer
	}
	rg.srv.d.CreateGitHubRepo = func(path, repo string) GitHubOutcome {
		f.createCalls = append(f.createCalls, [2]string{path, repo})
		return f.outcome
	}
	rg.srv.d.Now = func() time.Time { return f.now }
	return rg, f
}

// ghAnswerForm is the question view's POST.
func ghAnswerForm(path, answer string) string {
	v := url.Values{"confirm": {"yes"}, "path": {path}}
	if answer != "" {
		v.Set("github", answer)
	}
	return v.Encode()
}

// ghAsk makes the folder through the page, so it is on the offered list.
func ghAsk(t *testing.T, rg *rig) {
	t.Helper()
	if rec := rg.pageDo(http.MethodPost, "/plan/new", planNewForm("demo")); rec.Code != http.StatusOK {
		t.Fatalf("create: status = %d, want 200 (the question)\n%s", rec.Code, rec.Body.String())
	}
}

// TestPlanNewGitHubQuestion: a confirmed create with gh ready asks the
// question and starts nothing. Load-bearing: StartPlan and AddBeingPlanned
// are not reached (planning waits for the answer), the offer is asked once
// for the made folder, No is the checked choice, the form posts the made
// folder's full path with the confirm to /plan/new/github, and the API
// answers github_question with the stored repository name.
func TestPlanNewGitHubQuestion(t *testing.T) {
	t.Run("page", func(t *testing.T) {
		rg, f := ghRig(t)
		rec := rg.pageDo(http.MethodPost, "/plan/new", planNewForm("demo"))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200\n%s", rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		for _, w := range []string{
			`<form method="post" action="/plan/new/github" class="stack">`,
			`<input type="hidden" name="confirm" value="yes">`,
			`<input type="hidden" name="path" value="` + rg.createPath + `">`,
			`<legend class="field__label">Create a private GitHub repository for it?</legend>`,
			`<div class="choice-list">`,
			`<label class="choice"><input type="radio" name="github" value="yes"><span class="choice__label">Yes, create me/demo</span><span class="choice__meta">private</span></label>`,
			`<label class="choice"><input type="radio" name="github" value="no" checked><span class="choice__label">No, I'll add one later</span></label>`,
			"Nothing is pushed",
			`data-loading-label="Starting planning">Continue</button>`,
		} {
			if !strings.Contains(body, w) {
				t.Errorf("the page does not contain %q", w)
			}
		}
		if strings.Contains(body, "Cancel") {
			t.Errorf("the question offers a Cancel that would skip planning")
		}
		if rg.createCalls != 1 || len(f.offerCalls) != 1 || f.offerCalls[0] != rg.createPath {
			t.Errorf("create calls = %d, offer calls = %q, want one each for %s", rg.createCalls, f.offerCalls, rg.createPath)
		}
		if len(f.createCalls) != 0 {
			t.Errorf("a repository was made before the answer: %q", f.createCalls)
		}
		rg.noPlanNewSeams()
	})
	t.Run("api", func(t *testing.T) {
		rg, f := ghRig(t)
		rec := rg.do(http.MethodPost, "/api/plan/new", jsonType, `{"confirm": true, "path": "demo"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200\n%s", rec.Code, rec.Body.String())
		}
		m := decode(t, rec)
		gh, _ := m["github"].(map[string]any)
		if m["outcome"] != "github_question" || m["path"] != rg.createPath || m["created"] != true ||
			gh["repo"] != "me/demo" || gh["question"] != planNewGitHubQuestion {
			t.Errorf("body = %v", m)
		}
		if len(f.createCalls) != 0 {
			t.Errorf("a repository was made before the answer: %q", f.createCalls)
		}
		rg.noPlanNewSeams()
	})
}

// TestPlanNewGitHubAnswer: the answer, then planning, whatever the outcome.
// Load-bearing rows: "yes" makes exactly one repository, with the stored
// name, for the made folder, and starts once; "no" makes none and goes
// straight to the chat as before T8.15b; "name taken" and "failed" show the
// seam's sentence in the error style and still start once, because planning
// never waits on GitHub. "missing answer" proves no is the default.
func TestPlanNewGitHubAnswer(t *testing.T) {
	const taken = "GitHub already has a repository named me/demo, so none was made. The folder is left as it is."
	const failed = "Could not make the GitHub repository me/demo: dial tcp: lookup api.github.com: no such host. The folder is left as it is."
	const noOrigin = "Made the private GitHub repository me/demo, but could not set it as origin: remote origin already exists. Run `git remote add origin git@github.com:me/demo.git` in /x."
	tests := []struct {
		name        string
		answer      string // form value; the API row sends the JSON below
		json        string
		outcome     GitHubOutcome
		wantCreates int
		wantKind    string
		wantText    string
		wantClass   string // "" means the page redirects to the chat
	}{
		{name: "yes", answer: "yes", json: `true`, outcome: GitHubOutcome{Kind: GitHubCreated, Repo: "me/demo", Text: ghMade},
			wantCreates: 1, wantKind: GitHubCreated, wantText: ghMade, wantClass: "notice--ok"},
		{name: "no", answer: "no", json: `false`, wantKind: GitHubDeclined, wantText: planNewGitHubDeclined},
		{name: "missing answer", json: `null`, wantKind: GitHubDeclined, wantText: planNewGitHubDeclined},
		{name: "not a yes", answer: "YES please", json: `"yes"`, wantKind: GitHubDeclined, wantText: planNewGitHubDeclined},
		{name: "name taken", answer: "yes", json: `true`, outcome: GitHubOutcome{Kind: GitHubNameTaken, Repo: "me/demo", Text: taken},
			wantCreates: 1, wantKind: GitHubNameTaken, wantText: taken, wantClass: "notice--error"},
		{name: "failed", answer: "yes", json: `true`, outcome: GitHubOutcome{Kind: GitHubFailed, Repo: "me/demo", Text: failed},
			wantCreates: 1, wantKind: GitHubFailed, wantText: failed, wantClass: "notice--error"},
		{name: "no origin", answer: "yes", json: `true`, outcome: GitHubOutcome{Kind: GitHubNoOrigin, Repo: "me/demo", Text: noOrigin},
			wantCreates: 1, wantKind: GitHubNoOrigin, wantText: noOrigin, wantClass: "notice--error"},
	}
	for _, tc := range tests {
		check := func(t *testing.T, rg *rig, f *ghFake) {
			t.Helper()
			if len(f.createCalls) != tc.wantCreates {
				t.Fatalf("CreateGitHubRepo calls = %q, want %d", f.createCalls, tc.wantCreates)
			}
			if tc.wantCreates == 1 && f.createCalls[0] != [2]string{rg.createPath, "me/demo"} {
				t.Errorf("CreateGitHubRepo got %q, want (%s, me/demo)", f.createCalls[0], rg.createPath)
			}
			if len(rg.startCalls) != 1 || strings.Join(rg.startCalls[0], "|") != rg.createPath+"|new|" {
				t.Errorf("startCalls = %q, want one call (%s, new, \"\")", rg.startCalls, rg.createPath)
			}
			if len(rg.addedCalls) != 1 || rg.addedCalls[0] != rg.createPath {
				t.Errorf("addedCalls = %q, want [%s]", rg.addedCalls, rg.createPath)
			}
			t.Logf("creates=%d starts=%d", len(f.createCalls), len(rg.startCalls))
		}
		t.Run("page "+tc.name, func(t *testing.T) {
			rg, f := ghRig(t)
			f.outcome = tc.outcome
			ghAsk(t, rg)
			rec := rg.pageDo(http.MethodPost, "/plan/new/github", ghAnswerForm(rg.createPath, tc.answer))
			if tc.wantClass == "" {
				if rec.Code != http.StatusSeeOther {
					t.Fatalf("status = %d, want 303\n%s", rec.Code, rec.Body.String())
				}
				if got := rec.Header().Get("Location"); got != "/p/demo/plan/chat?done=started" {
					t.Errorf("Location = %q", got)
				}
			} else {
				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200\n%s", rec.Code, rec.Body.String())
				}
				body := rec.Body.String()
				for _, w := range []string{
					`<div class="notice ` + tc.wantClass + `"`,
					html.EscapeString(tc.wantText),
					"Planning started in " + rg.createPath + ".",
					`<a class="btn btn--primary" href="/p/demo/plan/chat">Open its chat</a>`,
				} {
					if !strings.Contains(body, w) {
						t.Errorf("the page does not contain %q", w)
					}
				}
			}
			check(t, rg, f)
		})
		t.Run("api "+tc.name, func(t *testing.T) {
			rg, f := ghRig(t)
			f.outcome = tc.outcome
			if rec := rg.do(http.MethodPost, "/api/plan/new", jsonType, `{"confirm": true, "path": "demo"}`); rec.Code != http.StatusOK {
				t.Fatalf("create: status = %d\n%s", rec.Code, rec.Body.String())
			}
			body := fmt.Sprintf(`{"confirm": true, "path": %q, "github": %s}`, rg.createPath, tc.json)
			rec := rg.do(http.MethodPost, "/api/plan/new/github", jsonType, body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200\n%s", rec.Code, rec.Body.String())
			}
			m := decode(t, rec)
			gh, _ := m["github"].(map[string]any)
			if m["path"] != rg.createPath || m["project"] != "demo" || m["chat"] != "/p/demo/plan/chat" || m["outcome"] != PlanStartStarted ||
				m["created"] != true || gh["outcome"] != tc.wantKind || gh["repo"] != "me/demo" || gh["text"] != tc.wantText {
				t.Errorf("body = %v", m)
			}
			check(t, rg, f)
		})
	}
}

// TestPlanNewGitHubNotOffered: gh not ready, so no question. Load-bearing:
// planning starts at once (one start), nothing is made on GitHub, and the
// offer's sentence (why, and what to run) reaches the owner, on the started
// view since a redirect cannot carry it and in the API's github object with
// outcome not_offered. "ask without a repository" proves an offer that names
// nothing is never asked.
func TestPlanNewGitHubNotOffered(t *testing.T) {
	const missing = "No GitHub repository offered: gh (the GitHub command) is not installed. Install it from https://cli.github.com, run `gh auth login`, then `gh repo create --private` in the folder."
	const loggedOut = "No GitHub repository offered: gh is not logged in. Run `gh auth login`, then `gh repo create --private` in the folder."
	const noAccount = "No GitHub repository offered: could not ask GitHub which account gh uses (HTTP 401)."
	tests := []struct {
		name       string
		offer      GitHubOffer
		wantReason string
		wantText   string
	}{
		{"gh missing", GitHubOffer{Reason: "missing", Notice: missing}, "missing", missing},
		{"logged out", GitHubOffer{Reason: "logged_out", Notice: loggedOut}, "logged_out", loggedOut},
		{"no account", GitHubOffer{Reason: "no_account", Notice: noAccount}, "no_account", noAccount},
		{"ask without a repository", GitHubOffer{Ask: true, Reason: "ask"}, "no_account", planNewGitHubNoNotice},
	}
	for _, tc := range tests {
		check := func(t *testing.T, rg *rig, f *ghFake) {
			t.Helper()
			if len(f.offerCalls) != 1 || len(f.createCalls) != 0 || len(rg.startCalls) != 1 || len(rg.addedCalls) != 1 {
				t.Errorf("offers %d, creates %d, starts %d, noted %d; want 1, 0, 1, 1", len(f.offerCalls), len(f.createCalls), len(rg.startCalls), len(rg.addedCalls))
			}
		}
		t.Run("page "+tc.name, func(t *testing.T) {
			rg, f := ghRig(t)
			f.offer = tc.offer
			rec := rg.pageDo(http.MethodPost, "/plan/new", planNewForm("demo"))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200\n%s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			for _, w := range []string{`<div class="notice notice--info" role="status">`, html.EscapeString(tc.wantText), `href="/p/demo/plan/chat">Open its chat</a>`} {
				if !strings.Contains(body, w) {
					t.Errorf("the page does not contain %q", w)
				}
			}
			check(t, rg, f)
		})
		t.Run("api "+tc.name, func(t *testing.T) {
			rg, f := ghRig(t)
			f.offer = tc.offer
			rec := rg.do(http.MethodPost, "/api/plan/new", jsonType, `{"confirm": true, "path": "demo"}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200\n%s", rec.Code, rec.Body.String())
			}
			m := decode(t, rec)
			gh, _ := m["github"].(map[string]any)
			if m["outcome"] != PlanStartStarted || m["created"] != true || gh["outcome"] != "not_offered" || gh["reason"] != tc.wantReason || gh["text"] != tc.wantText {
				t.Errorf("body = %v", m)
			}
			check(t, rg, f)
		})
	}
}

// TestPlanNewGitHubExistingFolder: a folder that was there before the form is
// never offered. Load-bearing: GitHubOffer is never called, the answer has no
// github key, the page goes straight to the chat, and a later answer for that
// folder is refused (it is not on the list).
func TestPlanNewGitHubExistingFolder(t *testing.T) {
	tests := []struct {
		name string
		kind string
	}{
		{"repo", PlanFolderRepo},
		{"plain folder", PlanFolderFolder},
		{"open session", PlanFolderOpen},
	}
	for _, tc := range tests {
		t.Run("page "+tc.name, func(t *testing.T) {
			rg, f := ghRig(t)
			rg.checkResult = PlanFolderCheck{Path: rg.createPath, Kind: tc.kind, Name: "demo"}
			rec := rg.pageDo(http.MethodPost, "/plan/new", planNewForm("demo"))
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want 303\n%s", rec.Code, rec.Body.String())
			}
			if rec := rg.pageDo(http.MethodPost, "/plan/new/github", ghAnswerForm(rg.createPath, "yes")); rec.Code != http.StatusBadRequest {
				t.Errorf("an answer for an existing folder: status = %d, want 400", rec.Code)
			}
			if len(f.offerCalls)+len(f.createCalls) != 0 || rg.createCalls != 0 || len(rg.startCalls) != 1 {
				t.Errorf("offers %d, gh creates %d, folder creates %d, starts %d; want 0, 0, 0, 1", len(f.offerCalls), len(f.createCalls), rg.createCalls, len(rg.startCalls))
			}
		})
		t.Run("api "+tc.name, func(t *testing.T) {
			rg, f := ghRig(t)
			rg.checkResult = PlanFolderCheck{Path: rg.createPath, Kind: tc.kind, Name: "demo"}
			m := decode(t, rg.do(http.MethodPost, "/api/plan/new", jsonType, `{"confirm": true, "path": "demo"}`))
			if _, ok := m["github"]; ok || m["created"] != false {
				t.Errorf("body = %v, want no github key", m)
			}
			if len(f.offerCalls) != 0 {
				t.Errorf("an existing folder was offered: %q", f.offerCalls)
			}
		})
	}
}

// TestPlanNewGitHubRefusals: answers the page will not act on. Load-bearing:
// every row makes nothing on GitHub and starts nothing. "not pending" is the
// crafted POST for a folder this server never made (400 not_offered);
// "answered twice" and "expired" prove the list is one-shot and an hour long;
// "no confirm" keeps the question answerable (a retry with the confirm
// works); "seams nil" refuses even a folder that was made, since without the
// seams no question was asked.
func TestPlanNewGitHubRefusals(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T, rg *rig, f *ghFake) string // returns the path answered
		body       string                                        // form body; "" means ghAnswerForm(path, "yes")
		wantReason string
		wantStarts int
	}{
		{name: "not pending", setup: func(t *testing.T, rg *rig, f *ghFake) string { return "/home/me/code/elsewhere" }, wantReason: "not_offered"},
		{name: "pending path differs", setup: func(t *testing.T, rg *rig, f *ghFake) string { ghAsk(t, rg); return rg.createPath + "-other" }, wantReason: "not_offered"},
		{name: "blank path", setup: func(t *testing.T, rg *rig, f *ghFake) string { ghAsk(t, rg); return "" }, wantReason: "not_offered"},
		{name: "answered twice", setup: func(t *testing.T, rg *rig, f *ghFake) string {
			ghAsk(t, rg)
			if rec := rg.pageDo(http.MethodPost, "/plan/new/github", ghAnswerForm(rg.createPath, "no")); rec.Code != http.StatusSeeOther {
				t.Fatalf("first answer: status = %d", rec.Code)
			}
			return rg.createPath
		}, wantReason: "not_offered", wantStarts: 1},
		{name: "expired", setup: func(t *testing.T, rg *rig, f *ghFake) string {
			ghAsk(t, rg)
			f.now = f.now.Add(gitHubOfferedTTL + time.Second)
			return rg.createPath
		}, wantReason: "not_offered"},
		{name: "seams nil", setup: func(t *testing.T, rg *rig, f *ghFake) string {
			ghAsk(t, rg)
			rg.srv.d.CreateGitHubRepo = nil
			return rg.createPath
		}, wantReason: "not_offered"},
		{name: "no confirm", setup: func(t *testing.T, rg *rig, f *ghFake) string { ghAsk(t, rg); return rg.createPath },
			body: "github=yes&path=PATH", wantReason: "bad_request"},
	}
	for _, tc := range tests {
		for _, api := range []bool{false, true} {
			name := "page "
			if api {
				name = "api "
			}
			t.Run(name+tc.name, func(t *testing.T) {
				rg, f := ghRig(t)
				path := tc.setup(t, rg, f)
				code := 0
				if api {
					body := fmt.Sprintf(`{"confirm": true, "path": %q, "github": true}`, path)
					if tc.body != "" {
						body = fmt.Sprintf(`{"path": %q, "github": true}`, path)
					}
					r := rg.do(http.MethodPost, "/api/plan/new/github", jsonType, body)
					code = r.Code
					if m := decode(t, r); m["reason"] != tc.wantReason {
						t.Errorf("body = %v, want reason %s", m, tc.wantReason)
					}
				} else {
					body := ghAnswerForm(path, "yes")
					if tc.body != "" {
						body = strings.Replace(tc.body, "PATH", url.QueryEscape(path), 1)
					}
					r := rg.pageDo(http.MethodPost, "/plan/new/github", body)
					code = r.Code
					want := planNewGitHubNotOffered
					if tc.wantReason == "bad_request" {
						want = planNewGitHubNoConfirm
					}
					if !strings.Contains(r.Body.String(), html.EscapeString(want)) {
						t.Errorf("the page does not say %q\n%s", want, r.Body.String())
					}
				}
				if code != http.StatusBadRequest {
					t.Errorf("status = %d, want 400", code)
				}
				if len(f.createCalls) != 0 || len(rg.startCalls) != tc.wantStarts {
					t.Errorf("creates %d, starts %d; want 0, %d", len(f.createCalls), len(rg.startCalls), tc.wantStarts)
				}
				t.Logf("status=%d creates=%d starts=%d", code, len(f.createCalls), len(rg.startCalls))
				if tc.name == "no confirm" {
					// The question is still answerable with the confirm.
					if r := rg.pageDo(http.MethodPost, "/plan/new/github", ghAnswerForm(rg.createPath, "no")); r.Code != http.StatusSeeOther {
						t.Errorf("a retry with the confirm: status = %d, want 303", r.Code)
					}
				}
			})
		}
	}
}

// TestPlanNewGitHubRepoFromRequestIgnored: the repository name is the stored
// one. Load-bearing: a crafted repo (or account) field in the answer never
// reaches CreateGitHubRepo, on either form.
func TestPlanNewGitHubRepoFromRequestIgnored(t *testing.T) {
	t.Run("page", func(t *testing.T) {
		rg, f := ghRig(t)
		ghAsk(t, rg)
		body := ghAnswerForm(rg.createPath, "yes") + "&repo=evil%2Fpwn&account=evil"
		rg.pageDo(http.MethodPost, "/plan/new/github", body)
		if len(f.createCalls) != 1 || f.createCalls[0][1] != "me/demo" {
			t.Errorf("CreateGitHubRepo calls = %q, want one with me/demo", f.createCalls)
		}
	})
	t.Run("api", func(t *testing.T) {
		rg, f := ghRig(t)
		ghAsk(t, rg)
		body := fmt.Sprintf(`{"confirm": true, "path": %q, "github": true, "repo": "evil/pwn"}`, rg.createPath)
		rg.do(http.MethodPost, "/api/plan/new/github", jsonType, body)
		if len(f.createCalls) != 1 || f.createCalls[0][1] != "me/demo" {
			t.Errorf("CreateGitHubRepo calls = %q, want one with me/demo", f.createCalls)
		}
	})
}

// TestPlanNewGitHubSeamsNil: without either seam the create behaves exactly
// as T8.15a did. Load-bearing: planning starts at once, the page redirects
// to the chat, and the API has no github key.
func TestPlanNewGitHubSeamsNil(t *testing.T) {
	tests := []struct {
		name string
		drop func(d *Deps)
	}{
		{"both nil", func(d *Deps) { d.GitHubOffer, d.CreateGitHubRepo = nil, nil }},
		{"offer nil", func(d *Deps) { d.GitHubOffer = nil }},
		{"create nil", func(d *Deps) { d.CreateGitHubRepo = nil }},
	}
	for _, tc := range tests {
		t.Run("page "+tc.name, func(t *testing.T) {
			rg, f := ghRig(t)
			tc.drop(&rg.srv.d)
			rec := rg.pageDo(http.MethodPost, "/plan/new", planNewForm("demo"))
			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/p/demo/plan/chat?done=started" {
				t.Errorf("status = %d, Location = %q; want 303 to the chat", rec.Code, rec.Header().Get("Location"))
			}
			if len(f.offerCalls)+len(f.createCalls) != 0 || len(rg.startCalls) != 1 {
				t.Errorf("offers %d, creates %d, starts %d; want 0, 0, 1", len(f.offerCalls), len(f.createCalls), len(rg.startCalls))
			}
		})
		t.Run("api "+tc.name, func(t *testing.T) {
			rg, _ := ghRig(t)
			tc.drop(&rg.srv.d)
			m := decode(t, rg.do(http.MethodPost, "/api/plan/new", jsonType, `{"confirm": true, "path": "demo"}`))
			if _, ok := m["github"]; ok || m["created"] != true || m["outcome"] != PlanStartStarted {
				t.Errorf("body = %v", m)
			}
		})
	}
}

// TestPlanNewGitHubStartRefused: a yes made the repository, then StartPlan
// refused. Load-bearing: the owner is told both, the folder left in place and
// the repository made, and the API's refusal keeps planNew's shape plus the
// github object.
func TestPlanNewGitHubStartRefused(t *testing.T) {
	rg, f := ghRig(t)
	ghAsk(t, rg)
	rg.startResult = PlanStartResult{Outcome: PlanStartRefused, Reason: "A planning session is already open."}
	body := fmt.Sprintf(`{"confirm": true, "path": %q, "github": true}`, rg.createPath)
	rec := rg.do(http.MethodPost, "/api/plan/new/github", jsonType, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	m := decode(t, rec)
	gh, _ := m["github"].(map[string]any)
	want := "A planning session is already open. The new folder " + rg.createPath + " was made and is left in place."
	if m["error"] != want || m["reason"] != "refused" || m["created"] != true || gh["outcome"] != GitHubCreated || gh["text"] != ghMade {
		t.Errorf("body = %v", m)
	}
	if len(f.createCalls) != 1 || len(rg.addedCalls) != 0 {
		t.Errorf("creates %d, noted %q; want 1 and none", len(f.createCalls), rg.addedCalls)
	}

	rg2, _ := ghRig(t)
	ghAsk(t, rg2)
	rg2.startResult = rg.startResult
	page := rg2.pageDo(http.MethodPost, "/plan/new/github", ghAnswerForm(rg2.createPath, "yes")).Body.String()
	for _, w := range []string{html.EscapeString("A planning session is already open."), html.EscapeString(ghMade)} {
		if !strings.Contains(page, w) {
			t.Errorf("the page does not say %q", w)
		}
	}
}

// TestPlanNewGitHubYes is the pure answer reading. Load-bearing: only
// github=yes in a form and "github": true in JSON are a yes; a missing,
// mistyped or string field is no, because nothing may be made without an
// explicit yes.
func TestPlanNewGitHubYes(t *testing.T) {
	tests := []struct {
		name string
		b    postBody
		want bool
	}{
		{"form yes", postBody{form: url.Values{"github": {"yes"}}}, true},
		{"form no", postBody{form: url.Values{"github": {"no"}}}, false},
		{"form missing", postBody{form: url.Values{}}, false},
		{"form Yes", postBody{form: url.Values{"github": {"Yes"}}}, false},
		{"json true", postBody{contentType: "application/json", json: []byte(`{"github": true}`)}, true},
		{"json false", postBody{contentType: "application/json", json: []byte(`{"github": false}`)}, false},
		{"json missing", postBody{contentType: "application/json", json: []byte(`{}`)}, false},
		{"json string yes", postBody{contentType: "application/json", json: []byte(`{"github": "yes"}`)}, false},
		{"json broken", postBody{contentType: "application/json", json: []byte(`{`)}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := gitHubYes(tc.b); got != tc.want {
				t.Errorf("gitHubYes = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPlanNewGitHubOfferedList is the pending list itself. Load-bearing: the
// list holds at most gitHubOfferedMax and drops the oldest, an entry lives
// exactly gitHubOfferedTTL, take is one-shot, and paths are compared clean.
func TestPlanNewGitHubOfferedList(t *testing.T) {
	tests := []struct {
		name   string
		run    func(o *gitHubOffered) (string, bool)
		want   bool
		wantRe string
	}{
		{"taken once", func(o *gitHubOffered) (string, bool) {
			o.add("/a", "me/a", testNow)
			return o.take("/a", testNow)
		}, true, "me/a"},
		{"taken twice", func(o *gitHubOffered) (string, bool) {
			o.add("/a", "me/a", testNow)
			o.take("/a", testNow)
			return o.take("/a", testNow)
		}, false, ""},
		{"clean path", func(o *gitHubOffered) (string, bool) {
			o.add("/a/b", "me/b", testNow)
			return o.take("/a/./b/", testNow)
		}, true, "me/b"},
		{"at the hour", func(o *gitHubOffered) (string, bool) {
			o.add("/a", "me/a", testNow)
			return o.take("/a", testNow.Add(gitHubOfferedTTL))
		}, true, "me/a"},
		{"past the hour", func(o *gitHubOffered) (string, bool) {
			o.add("/a", "me/a", testNow)
			return o.take("/a", testNow.Add(gitHubOfferedTTL+time.Nanosecond))
		}, false, ""},
		{"oldest dropped when full", func(o *gitHubOffered) (string, bool) {
			for i := 0; i <= gitHubOfferedMax; i++ {
				o.add(fmt.Sprintf("/f%d", i), "me/x", testNow.Add(time.Duration(i)*time.Second))
			}
			if len(o.entries) != gitHubOfferedMax {
				return "count", true
			}
			return o.take("/f0", testNow.Add(time.Minute))
		}, false, ""},
		{"newest kept when full", func(o *gitHubOffered) (string, bool) {
			for i := 0; i <= gitHubOfferedMax; i++ {
				o.add(fmt.Sprintf("/f%d", i), fmt.Sprintf("me/%d", i), testNow.Add(time.Duration(i)*time.Second))
			}
			return o.take(fmt.Sprintf("/f%d", gitHubOfferedMax), testNow.Add(time.Minute))
		}, true, fmt.Sprintf("me/%d", gitHubOfferedMax)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo, ok := tc.run(newGitHubOffered())
			if ok != tc.want || repo != tc.wantRe {
				t.Errorf("take = (%q, %v), want (%q, %v)", repo, ok, tc.wantRe, tc.want)
			}
		})
	}
}
