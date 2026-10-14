// Package ghrepo decides whether the dashboard offers to make a GitHub
// repository for a folder it has just made (T7.6), words the outcome, and
// makes the repository.
//
// The offer is made every time, never remembered: the owner's profile says
// "ask before opening a new one", so a saved yes would go against it. The
// repository is always private, and nothing is pushed: the owner reviews the
// plan before any code leaves the machine. The package has its own small GH
// and Git interfaces rather than extending ship.GH, whose method set is fixed
// and faked across internal/ship.
package ghrepo

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/dphaener/afk/internal/ship"
)

// GH is the part of the gh command this package uses.
type GH interface {
	// AuthStatus returns nil when gh is logged in.
	AuthStatus() error
	// Login returns the account name gh is logged in as.
	Login() (string, error)
	// Protocol returns "ssh" or "https", whichever gh clones with.
	Protocol() string
	// CreatePrivate makes the private repository (OWNER/NAME) and returns
	// the URL gh printed for it.
	CreatePrivate(repo string) (url string, err error)
}

// Git is the part of git this package uses.
type Git interface {
	// AddOrigin adds url as the remote named origin of the repository in dir.
	AddOrigin(dir, url string) error
}

// Reason says why Offer did or did not ask.
type Reason string

const (
	// ReasonMissing: gh is not installed.
	ReasonMissing Reason = "missing"
	// ReasonLoggedOut: gh is installed but not logged in.
	ReasonLoggedOut Reason = "logged_out"
	// ReasonNoAccount: gh could not say which account it uses.
	ReasonNoAccount Reason = "no_account"
	// ReasonAsk: everything is ready, so ask.
	ReasonAsk Reason = "ask"
)

// Decision is what Offer concludes.
type Decision struct {
	Ask    bool
	Repo   string // account/folder name; set only when Ask
	Reason Reason
	Notice string // the line to show when Ask is false
}

// Offer decides whether to ask. The impure checks (looking gh up, running
// auth status and the account query) happen in the caller and arrive as
// parameters, so the whole matrix is table-testable. The checks are ordered
// as the owner would fix them: install, then log in, then the account.
func Offer(lookErr, authErr error, login string, loginErr error, folder string) Decision {
	switch {
	case lookErr != nil:
		return Decision{Reason: ReasonMissing, Notice: "No GitHub repository offered: gh (the GitHub command) is not installed. Install it from https://cli.github.com, run `gh auth login`, then `gh repo create --private` in the folder."}
	case authErr != nil:
		return Decision{Reason: ReasonLoggedOut, Notice: "No GitHub repository offered: gh is not logged in. Run `gh auth login`, then `gh repo create --private` in the folder."}
	case loginErr != nil || strings.TrimSpace(login) == "":
		why := "gh gave no account name"
		if loginErr != nil {
			why = firstLine(errText(loginErr))
		}
		return Decision{Reason: ReasonNoAccount, Notice: "No GitHub repository offered: could not ask GitHub which account gh uses (" + why + ")."}
	}
	return Decision{Ask: true, Reason: ReasonAsk, Repo: strings.TrimSpace(login) + "/" + filepath.Base(folder)}
}

// Kind names how a create attempt ended.
type Kind string

const (
	OutcomeCreated   Kind = "created"
	OutcomeDeclined  Kind = "declined"
	OutcomeNameTaken Kind = "name_taken"
	OutcomeFailed    Kind = "failed"
	OutcomeNoOrigin  Kind = "no_origin"
)

// Outcome is how an offer ended.
type Outcome struct {
	Kind Kind
	Repo string
	URL  string // the remote URL, for OutcomeNoOrigin
	Dir  string
	Err  error
}

// DeclinedText is the line shown when the owner says no.
const DeclinedText = "No GitHub repository was made."

// OutcomeText words an outcome as one line in plain language.
func OutcomeText(o Outcome) string {
	switch o.Kind {
	case OutcomeCreated:
		return fmt.Sprintf("Made the private GitHub repository %s and set it as origin. Nothing was pushed.", o.Repo)
	case OutcomeDeclined:
		return DeclinedText
	case OutcomeNameTaken:
		return fmt.Sprintf("GitHub already has a repository named %s, so none was made. The folder is left as it is.", o.Repo)
	case OutcomeFailed:
		return fmt.Sprintf("Could not make the GitHub repository %s: %s. The folder is left as it is.", o.Repo, firstLine(errText(o.Err)))
	case OutcomeNoOrigin:
		return fmt.Sprintf("Made the private GitHub repository %s, but could not set it as origin: %s. Run `git remote add origin %s` in %s.", o.Repo, firstLine(errText(o.Err)), o.URL, o.Dir)
	}
	return ""
}

// Create makes the private repository and sets it as origin in dir. It calls
// CreatePrivate once and AddOrigin at most once, and never pushes.
func Create(gh GH, git Git, dir, repo string) Outcome {
	out, err := gh.CreatePrivate(repo)
	if err != nil {
		if strings.Contains(strings.ToLower(errText(err)), "already exists") {
			return Outcome{Kind: OutcomeNameTaken, Repo: repo, Dir: dir, Err: err}
		}
		return Outcome{Kind: OutcomeFailed, Repo: repo, Dir: dir, Err: err}
	}
	url := remoteURL(out, gh.Protocol())
	if err := git.AddOrigin(dir, url); err != nil {
		return Outcome{Kind: OutcomeNoOrigin, Repo: repo, URL: url, Dir: dir, Err: err}
	}
	return Outcome{Kind: OutcomeCreated, Repo: repo, URL: url, Dir: dir}
}

// remoteURL turns the URL gh printed (the last non-empty line; gh may print
// notices before it) into the remote to add, in the protocol gh clones with.
func remoteURL(printed, protocol string) string {
	url := ""
	for _, l := range strings.Split(printed, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			url = l
		}
	}
	url = strings.TrimSuffix(url, ".git")
	rest := strings.TrimPrefix(url, "https://")
	if rest == url { // not an https URL: use it as printed
		return url
	}
	if protocol == "ssh" {
		if host, path, ok := strings.Cut(rest, "/"); ok {
			return "git@" + host + ":" + path + ".git"
		}
	}
	return url + ".git"
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	var ce *ship.CmdError
	if errors.As(err, &ce) && strings.TrimSpace(ce.Stderr) != "" {
		return strings.TrimSpace(ce.Stderr)
	}
	return err.Error()
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

type realGH struct{ run ship.RunFunc }

// NewGH returns the gh adapter over run. The create takes no flag but
// --private: no --push, --public, --internal, --clone or --source, so the
// command cannot publish anything or touch the folder.
func NewGH(run ship.RunFunc) GH { return realGH{run} }

func (g realGH) AuthStatus() error {
	_, err := g.run("", "gh", "auth", "status")
	return err
}

func (g realGH) Login() (string, error) {
	out, err := g.run("", "gh", "api", "user", "--jq", ".login")
	return strings.TrimSpace(out), err
}

func (g realGH) Protocol() string {
	out, err := g.run("", "gh", "config", "get", "git_protocol")
	if err != nil || strings.TrimSpace(out) != "ssh" {
		return "https"
	}
	return "ssh"
}

func (g realGH) CreatePrivate(repo string) (string, error) {
	return g.run("", "gh", "repo", "create", repo, "--private")
}

type realGit struct{ run ship.RunFunc }

// NewGit returns the git adapter over run.
func NewGit(run ship.RunFunc) Git { return realGit{run} }

func (g realGit) AddOrigin(dir, url string) error {
	_, err := g.run(dir, "git", "remote", "add", "origin", url)
	return err
}
