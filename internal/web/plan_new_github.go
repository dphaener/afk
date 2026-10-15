package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// This file is the GitHub question of Plan a new project (T8.15b). After the
// page makes a folder for a bare name (T8.15a) and gh is ready, the owner is
// asked whether to create a private GitHub repository for it and set it as
// origin, the same question the dashboard asks (T7.6). The answer is a POST
// of its own, planNewGitHub, which makes the repository only on yes and then
// starts planning whatever the answer.
//
// Four rules hold here.
//
// Nothing is made on GitHub without an explicit yes. No is the checked
// choice, and a missing or unreadable answer means no. The owner's profile
// says to ask before opening a repository, so the question is asked every
// time and never remembered.
//
// Only a folder this server has just made is offered. The question is a
// separate request from the create, so a crafted POST could otherwise name
// any folder on the machine. The server records each folder it made and asked
// about (gitHubOffered) and acts only on those: a folder that existed before
// the form, one asked about more than an hour ago, or one already answered
// gets nothing.
//
// The repository name is never the request's. It is the one GitHubOffer gave
// when the question was asked, kept with the folder, so an answer cannot
// make a repository under another name or account.
//
// Nothing is pushed. Deps.CreateGitHubRepo makes the repository and sets
// origin; the plan's first push is the build's, much later.

// GitHubOffer is what Deps.GitHubOffer says about a folder just made. It is
// this package's own plain copy of internal/ghrepo's decision, because this
// package never imports it.
type GitHubOffer struct {
	// Ask is whether gh is ready, so the question can be asked.
	Ask bool
	// Repo is the account/folder name the question offers; set only when
	// Ask.
	Repo string
	// Reason is why the question is or is not asked: missing, logged_out,
	// no_account or ask, the same words as internal/ghrepo's.
	Reason string
	// Notice is the plain sentence shown when Ask is false: why no
	// repository is offered and what to run.
	Notice string
}

// GitHubOutcome is what Deps.CreateGitHubRepo says about a create. It is this
// package's own plain copy of internal/ghrepo's outcome.
type GitHubOutcome struct {
	// Kind is one of the GitHub constants below, the same words as
	// internal/ghrepo's Kind.
	Kind string
	// Repo is the repository's account/folder name.
	Repo string
	// Text is the outcome as one plain sentence.
	Text string
}

// The kinds a GitHubOutcome takes.
const (
	GitHubCreated   = "created"
	GitHubDeclined  = "declined"
	GitHubNameTaken = "name_taken"
	GitHubFailed    = "failed"
	GitHubNoOrigin  = "no_origin"
)

// planNewGitHubWriteLimit is how long the answer to a POST that runs gh may
// take to write. The server's write timeout is 30 seconds, but gh may take up
// to 30 seconds per call (the offer runs up to three, the create two, and
// planning starts after), so without a longer deadline the owner would get a
// dropped connection after a create that in fact went through. Two minutes
// covers the slowest of them with room for the start.
const planNewGitHubWriteLimit = 2 * time.Minute

// gitHubOfferedTTL is how long a question stays answerable. The record exists
// only to prove the folder was made by this form moments ago; an hour covers
// an owner who walked away from the phone, and anything older is refused
// (the folder can still be planned by typing its path).
const gitHubOfferedTTL = time.Hour

// gitHubOfferedMax is the most questions remembered at once; the oldest is
// dropped to make room. Each needs a confirmed create, so 32 is far more than
// one owner leaves open, and small enough that no flood can fill memory.
const gitHubOfferedMax = 32

// The sentences this file says.
const (
	planNewGitHubQuestion = "Create a private GitHub repository for it?"
	planNewGitHubLoading  = "Starting planning"
	// planNewGitHubDeclined is internal/ghrepo's DeclinedText, copied
	// because this package never imports it.
	planNewGitHubDeclined   = "No GitHub repository was made."
	planNewGitHubNoConfirm  = "Nothing was made on GitHub and nothing was started: answering the GitHub question needs the confirm."
	planNewGitHubUnreadable = `Nothing was made on GitHub and nothing was started: the request could not be read. Send it as JSON like {"path": "/home/me/code/demo", "github": false, "confirm": true}.`
	planNewGitHubNotOffered = "Nothing was made on GitHub and nothing was started: this page did not just make that folder, or its GitHub question was already answered or is more than an hour old. To plan in the folder, type its full path above."
	planNewGitHubNoNotice   = "No GitHub repository offered: afk could not tell which GitHub account to make it under."
)

// gitHubOffered is the folders this server made through CreatePlanFolder and
// asked the GitHub question about, with the repository name it offered, not
// yet answered.
//
// It is memory and not a file on purpose. It exists only to prove that a
// folder was made by this form moments ago, so an answer can be trusted to
// name it; a file would outlive that proof and could be written by anything
// else on the machine. A restart that forgets it is the safe failure: the
// answer is refused, nothing is made on GitHub, and the folder can still be
// planned by typing its path.
type gitHubOffered struct {
	mu      sync.Mutex
	entries map[string]gitHubOfferedEntry
}

type gitHubOfferedEntry struct {
	repo string
	at   time.Time
}

func newGitHubOffered() *gitHubOffered {
	return &gitHubOffered{entries: map[string]gitHubOfferedEntry{}}
}

// add records that path was made and asked about at now, offering repo. A
// full list drops its oldest entry first, so a new question always fits.
func (o *gitHubOffered) add(path, repo string, now time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.expire(now)
	key := filepath.Clean(path)
	if _, ok := o.entries[key]; !ok {
		for len(o.entries) >= gitHubOfferedMax {
			oldest, first := "", true
			for k, e := range o.entries {
				if first || e.at.Before(o.entries[oldest].at) {
					oldest, first = k, false
				}
			}
			delete(o.entries, oldest)
		}
	}
	o.entries[key] = gitHubOfferedEntry{repo: repo, at: now}
}

// take answers the repository offered for path and removes the entry,
// whatever the answer will be, so a question can be answered once. An entry
// older than gitHubOfferedTTL is gone, as if never made.
func (o *gitHubOffered) take(path string, now time.Time) (repo string, ok bool) {
	if strings.TrimSpace(path) == "" {
		return "", false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.expire(now)
	key := filepath.Clean(path)
	e, ok := o.entries[key]
	if !ok {
		return "", false
	}
	delete(o.entries, key)
	return e.repo, true
}

// expire drops every entry older than gitHubOfferedTTL. The caller holds mu.
func (o *gitHubOffered) expire(now time.Time) {
	for k, e := range o.entries {
		if now.Sub(e.at) > gitHubOfferedTTL {
			delete(o.entries, k)
		}
	}
}

// canAskGitHub reports whether the page asks the GitHub question at all. Both
// seams are needed: an offer without a create would ask a question a yes
// could not carry out, and a create without the offer would have no
// repository name to make.
func (s *server) canAskGitHub() bool {
	return s.d.GitHubOffer != nil && s.d.CreateGitHubRepo != nil
}

// planNewGitHubNote is the GitHub line an answer carries: json is the API's
// github object; text, class and role the page's line on the started view. A
// quiet note (declined) draws no line, so the page may go straight to the
// chat as before T8.15b.
type planNewGitHubNote struct {
	json        map[string]any
	text        string
	class, role string
	quiet       bool
}

// notOfferedNote is the line for a folder made while gh was not ready. Pure.
func notOfferedNote(offer GitHubOffer) *planNewGitHubNote {
	text, reason := offer.Notice, offer.Reason
	if text == "" {
		text = planNewGitHubNoNotice
	}
	if reason == "" || reason == "ask" {
		// An offer that said ask but named no repository cannot be asked;
		// it is reported as the account it could not name.
		reason = "no_account"
	}
	return &planNewGitHubNote{
		json: map[string]any{"outcome": "not_offered", "reason": reason, "text": text},
		text: text, class: "notice--info", role: "status",
	}
}

// answerNote is the line for an answered question. Pure. repo is the stored
// name, used when the seam's outcome names none. A made repository is a plain
// notice, a declined one is quiet, and every other kind (name taken, failed,
// no origin, or a kind this page does not know) is drawn as an error: in each
// the owner has something to do.
func answerNote(out GitHubOutcome, repo string) *planNewGitHubNote {
	if out.Repo == "" {
		out.Repo = repo
	}
	if out.Kind == "" {
		out.Kind = GitHubFailed
	}
	if out.Text == "" {
		switch out.Kind {
		case GitHubDeclined:
			out.Text = planNewGitHubDeclined
		default:
			out.Text = "afk could not tell how making the GitHub repository " + out.Repo + " went. Run `gh repo view " + out.Repo + "` to see whether it was made."
		}
	}
	n := &planNewGitHubNote{
		json: map[string]any{"outcome": out.Kind, "repo": out.Repo, "text": out.Text},
		text: out.Text, class: "notice--error", role: "alert",
	}
	switch out.Kind {
	case GitHubCreated:
		n.class, n.role = "notice--ok", "status"
	case GitHubDeclined:
		n.quiet = true
	}
	return n
}

// gitHubYes is whether the answer's body says yes. Pure. A form says yes only
// with github=yes and JSON only with "github": true; anything else, a missing
// field included, is no, because nothing may be made on GitHub without an
// explicit yes.
func gitHubYes(b postBody) bool {
	if b.contentType != "application/json" {
		return b.form.Get("github") == "yes"
	}
	var body struct {
		GitHub json.RawMessage `json:"github"`
	}
	if err := json.Unmarshal(b.json, &body); err != nil || body.GitHub == nil {
		return false
	}
	var yes bool
	return json.Unmarshal(body.GitHub, &yes) == nil && yes
}

// askGitHub answers the create's POST with the question and starts nothing:
// the API with outcome github_question, the page with the question view.
func (s *server) askGitHub(w http.ResponseWriter, r *http.Request, path, repo string) {
	if isAPI(r.URL.Path) {
		writeJSON(w, http.StatusOK, map[string]any{
			"outcome": "github_question", "path": path, "created": true,
			"github": map[string]any{"repo": repo, "question": planNewGitHubQuestion},
		})
		return
	}
	s.renderPlanNew(w, r, http.StatusOK, NewPlanPageView{
		Kind: newPlanKindGitHub, Path: path, Repo: repo, Question: planNewGitHubQuestion, LoadingLabel: planNewGitHubLoading,
	})
}

// planNewGitHub is POST /plan/new/github and /api/plan/new/github: the
// answer to the GitHub question. Only with the confirm, and only for a folder
// on the offered list (which it takes, whatever the answer), it makes the
// repository on yes through Deps.CreateGitHubRepo with the stored name, and
// then starts planning exactly as planNew does, whatever the GitHub outcome.
func (s *server) planNewGitHub(w http.ResponseWriter, r *http.Request) {
	if !s.canPlanNew() {
		s.notFound(w, r)
		return
	}
	b, err := readPostBody(r)
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		s.tooLarge(w, r)
		return
	}
	if err != nil || !b.confirmed() {
		// The entry is kept: the owner's own form always carries the
		// confirm, so this is no answer, and pressing Continue again works.
		t, _ := bodyField(b, "path")
		s.planNewBad(w, r, strings.TrimSpace(t), planNewGitHubNoConfirm)
		return
	}
	path, ok := bodyField(b, "path")
	if !ok {
		s.planNewBad(w, r, "", planNewGitHubUnreadable)
		return
	}
	path = strings.TrimSpace(path)
	repo, ok := "", false
	if s.canAskGitHub() {
		repo, ok = s.offered.take(path, s.d.Now())
	}
	if !ok {
		s.planNewRefuse(w, r, path, planNewGitHubNotOffered, "not_offered", path)
		return
	}
	var note *planNewGitHubNote
	if gitHubYes(b) {
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(planNewGitHubWriteLimit))
		note = answerNote(s.d.CreateGitHubRepo(path, repo), repo)
	} else {
		note = answerNote(GitHubOutcome{Kind: GitHubDeclined, Repo: repo, Text: planNewGitHubDeclined}, repo)
	}
	// The folder was made by this form (the entry proves it), so the
	// answer says created like planNew's would have.
	s.startPlanNew(w, r, path, path, true, note)
}
