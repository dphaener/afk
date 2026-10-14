package ghrepo

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/ship"
)

// TestOffer: "ask" proves the repo name is account/folder-name; "missing wins
// over logged out" proves the order the owner would fix things in; the empty
// login row proves a blank answer is not turned into "/demo".
func TestOffer(t *testing.T) {
	boom := errors.New("network down\nmore")
	cases := []struct {
		name    string
		look    error
		auth    error
		login   string
		loginEr error
		want    Reason
		repo    string
		notice  string
	}{
		{"ask", nil, nil, "owner", nil, ReasonAsk, "owner/demo", ""},
		{"gh missing", exec.ErrNotFound, nil, "owner", nil, ReasonMissing, "", "not installed"},
		{"logged out", nil, boom, "owner", nil, ReasonLoggedOut, "", "gh auth login"},
		{"no account", nil, nil, "", boom, ReasonNoAccount, "", "(network down)"},
		{"empty login", nil, nil, "  ", nil, ReasonNoAccount, "", "gave no account"},
		{"missing wins over logged out", exec.ErrNotFound, boom, "", boom, ReasonMissing, "", "https://cli.github.com"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := c.look
			got := Offer(d, c.auth, c.login, c.loginEr, "/x/y/demo")
			if got.Reason != c.want || got.Ask != (c.want == ReasonAsk) || got.Repo != c.repo {
				t.Fatalf("got %+v", got)
			}
			if !strings.Contains(got.Notice, c.notice) {
				t.Fatalf("notice %q lacks %q", got.Notice, c.notice)
			}
			if !got.Ask && !strings.Contains(got.Notice, "`gh") && got.Reason != ReasonNoAccount {
				t.Fatalf("notice names no command: %q", got.Notice)
			}
		})
	}
}

// TestOutcomeText: every kind is a row; the failure rows must carry only the
// first line of the error and say the folder is left alone.
func TestOutcomeText(t *testing.T) {
	cases := []struct {
		name string
		o    Outcome
		want string
	}{
		{"created", Outcome{Kind: OutcomeCreated, Repo: "o/d"}, "Made the private GitHub repository o/d and set it as origin. Nothing was pushed."},
		{"declined", Outcome{Kind: OutcomeDeclined}, "No GitHub repository was made."},
		{"name taken", Outcome{Kind: OutcomeNameTaken, Repo: "o/d"}, "GitHub already has a repository named o/d, so none was made. The folder is left as it is."},
		{"network error", Outcome{Kind: OutcomeFailed, Repo: "o/d", Err: errors.New("timeout\nsecond line")}, "Could not make the GitHub repository o/d: timeout. The folder is left as it is."},
		{"no origin", Outcome{Kind: OutcomeNoOrigin, Repo: "o/d", URL: "https://github.com/o/d.git", Dir: "/p/d", Err: errors.New("fatal: x\ny")}, "Made the private GitHub repository o/d, but could not set it as origin: fatal: x. Run `git remote add origin https://github.com/o/d.git` in /p/d."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := OutcomeText(c.o); got != c.want {
				t.Fatalf("got %q\nwant %q", got, c.want)
			}
		})
	}
}

type fakeGH struct {
	proto   string
	url     string
	err     error
	creates []string
}

func (f *fakeGH) AuthStatus() error      { return nil }
func (f *fakeGH) Login() (string, error) { return "owner", nil }
func (f *fakeGH) Protocol() string       { return f.proto }
func (f *fakeGH) CreatePrivate(r string) (string, error) {
	f.creates = append(f.creates, r)
	return f.url, f.err
}

type fakeGit struct {
	err  error
	urls []string
	dirs []string
}

func (f *fakeGit) AddOrigin(dir, url string) error {
	f.dirs = append(f.dirs, dir)
	f.urls = append(f.urls, url)
	return f.err
}

// TestCreate: the failure rows (name taken, network error) are load-bearing:
// they must make zero AddOrigin calls, or a failed create would leave a
// remote that points at nothing. Every row makes exactly one CreatePrivate.
func TestCreate(t *testing.T) {
	cases := []struct {
		name    string
		gh      fakeGH
		git     fakeGit
		kind    Kind
		origins []string
	}{
		{"yes https", fakeGH{proto: "https", url: "https://github.com/owner/demo\n"}, fakeGit{}, OutcomeCreated, []string{"https://github.com/owner/demo.git"}},
		{"yes ssh", fakeGH{proto: "ssh", url: "notice\nhttps://github.com/owner/demo"}, fakeGit{}, OutcomeCreated, []string{"git@github.com:owner/demo.git"}},
		{"name taken", fakeGH{err: errors.New("GraphQL: Name already exists on this account")}, fakeGit{}, OutcomeNameTaken, nil},
		{"network error", fakeGH{err: errors.New("dial tcp: no route")}, fakeGit{}, OutcomeFailed, nil},
		{"add origin fails", fakeGH{proto: "https", url: "https://github.com/owner/demo"}, fakeGit{err: errors.New("remote origin already exists")}, OutcomeNoOrigin, []string{"https://github.com/owner/demo.git"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gh, git := c.gh, c.git
			o := Create(&gh, &git, "/p/demo", "owner/demo")
			if o.Kind != c.kind {
				t.Fatalf("kind %s, want %s", o.Kind, c.kind)
			}
			if len(gh.creates) != 1 || gh.creates[0] != "owner/demo" {
				t.Fatalf("creates = %v", gh.creates)
			}
			if strings.Join(git.urls, ",") != strings.Join(c.origins, ",") {
				t.Fatalf("origins = %v, want %v", git.urls, c.origins)
			}
			for _, d := range git.dirs {
				if d != "/p/demo" {
					t.Fatalf("dir %q", d)
				}
			}
		})
	}
}

// TestRealGHArgv: the create must carry --private and none of the flags that
// would publish, push or touch the folder.
func TestRealGHArgv(t *testing.T) {
	var calls [][]string
	run := ship.RunFunc(func(dir, name string, args ...string) (string, error) {
		calls = append(calls, append([]string{name}, args...))
		if len(args) > 1 && args[0] == "config" {
			return "", errors.New("unset")
		}
		return "https://github.com/owner/demo\n", nil
	})
	gh := NewGH(run)
	if gh.Protocol() != "https" {
		t.Fatal("protocol should default to https")
	}
	if _, err := gh.CreatePrivate("owner/demo"); err != nil {
		t.Fatal(err)
	}
	if err := NewGit(run).AddOrigin("/p", "u"); err != nil {
		t.Fatal(err)
	}
	last := strings.Join(calls[1], " ")
	if last != "gh repo create owner/demo --private" {
		t.Fatalf("create argv: %s", last)
	}
	for _, c := range calls {
		j := " " + strings.Join(c, " ") + " "
		for _, bad := range []string{"--push", "--public", "--internal", "--clone", "--source"} {
			if strings.Contains(j, " "+bad+" ") {
				t.Fatalf("%s in %s", bad, j)
			}
		}
	}
	if got := strings.Join(calls[2], " "); got != "git remote add origin u" {
		t.Fatalf("git argv: %s", got)
	}
}
