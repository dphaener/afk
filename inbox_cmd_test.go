package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/daemonconfig"
	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/notify"
	"github.com/dphaener/afk/internal/projectconfig"
)

// inboxTestIDs are the ids the scripted generator hands out, in order. They
// are written out so a row can name the item it expects.
var inboxTestIDs = []string{"aaaaaa", "bbbbbb", "cccccc", "dddddd", "eeeeee", "ffffff"}

const inboxTestURL = "https://ntfy.example/afk-topic"

type inboxSend struct {
	url string
	msg notify.NtfyMessage
}

// inboxFixture is one hermetic `afk inbox` world: a temp folder standing in
// for the project's main checkout, a clock that moves one minute per reading
// (so list order is decided by time, not by the id), scripted ids and fakes
// for git, the glob, afk.toml and the notification.
type inboxFixture struct {
	t       *testing.T
	project string
	env     inboxEnv
	stdout  bytes.Buffer
	stderr  bytes.Buffer

	ntfyURL   string
	configErr error
	sendErr   error
	statuses  []string

	sends       []inboxSend
	configRoots []string
	globs       []string

	// machine and machineErr are what the fake machine config read answers;
	// machineLoads counts the reads. A new fixture has no machine config, as
	// most machines do.
	machine      daemonconfig.Config
	machineErr   error
	machineLoads int
}

func newInboxFixture(t *testing.T) *inboxFixture {
	t.Helper()
	f := &inboxFixture{t: t, project: t.TempDir(), machineErr: errors.New("no config.toml in tests")}
	tick, next := 0, 0
	f.env = inboxEnv{
		stdout: &f.stdout,
		stderr: &f.stderr,
		cwd:    f.project,
		home:   "",
		now: func() time.Time {
			tick++
			return time.Date(2026, 10, 11, 9, tick, 0, 0, time.UTC)
		},
		newID: func() string {
			id := inboxTestIDs[next%len(inboxTestIDs)]
			next++
			return id
		},
		gitCommonDir: func(string) string { return filepath.Join(f.project, ".git") },
		repoRoot:     func(dir string) string { return dir },
		glob: func(pattern string) ([]string, error) {
			f.globs = append(f.globs, pattern)
			return f.statuses, nil
		},
		loadConfig: func(root string) (projectconfig.Config, error) {
			f.configRoots = append(f.configRoots, root)
			if f.configErr != nil {
				return projectconfig.Config{}, f.configErr
			}
			return projectconfig.Config{Notify: projectconfig.Notify{NtfyURL: f.ntfyURL}}, nil
		},
		send: func(_ context.Context, url string, m notify.NtfyMessage) error {
			f.sends = append(f.sends, inboxSend{url: url, msg: m})
			return f.sendErr
		},
		loadMachine: func() (daemonconfig.Config, error) {
			f.machineLoads++
			return f.machine, f.machineErr
		},
	}
	return f
}

// run calls runInbox with fresh output buffers and returns the exit code.
func (f *inboxFixture) run(args ...string) int {
	f.t.Helper()
	f.stdout.Reset()
	f.stderr.Reset()
	return runInbox(args, f.env)
}

// mustRun is for a row's setup: the command has to succeed.
func (f *inboxFixture) mustRun(args ...string) {
	f.t.Helper()
	if code := f.run(args...); code != 0 {
		f.t.Fatalf("setup: afk inbox %s exited %d: %s", strings.Join(args, " "), code, f.stderr.String())
	}
}

// inboxItemFiles returns the item files under root's .afk/inbox/, sorted.
func inboxItemFiles(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(inbox.Dir(root))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		// A row may replace .afk with a plain file to force a write error.
		return nil
	}
	var names []string
	for _, e := range entries {
		if id, ok := strings.CutSuffix(e.Name(), ".json"); ok && inbox.ValidID(id) {
			names = append(names, e.Name())
		}
	}
	return names
}

func readInboxItem(t *testing.T, root, id string) inbox.Item {
	t.Helper()
	it, err := inbox.Store{Dir: inbox.Dir(root)}.Get(id)
	if err != nil {
		t.Fatalf("reading item %s under %s: %v", id, root, err)
	}
	return it
}

func wantContains(t *testing.T, what, got string, fragments []string) {
	t.Helper()
	for _, frag := range fragments {
		if !strings.Contains(got, frag) {
			t.Errorf("%s missing %q:\n%s", what, frag, got)
		}
	}
}

// TestRunInbox drives the whole command over argv rows and checks the exit
// code, what was printed, the item files on disk and how many notifications
// went out.
//
// Load-bearing rows:
//
//   - (a) "blocking item with an address sends once": the one thing the
//     notification exists for. Exactly once, because a retry or a second call
//     would buzz the owner's phone twice for one parked task.
//   - (b) "no address sends nothing": most projects have no afk.toml, and
//     that must be silence with exit 0, not a warning on every add.
//   - (c) "send error still exits 0" and (d) "afk.toml error still exits 0":
//     the caller is an agent about to park on the item. A non-zero exit after
//     the file is written would tell it the item was not recorded.
//   - (e) "from a linked worktree the item lands in the main checkout": the
//     reason mainCheckout exists. Written into the worktree's own .afk/, the
//     item would be invisible to the owner and deleted with the worktree.
//   - (f) "neither block flag" and "both block flags": there is no safe
//     default, so a forgotten flag is exit 2 with nothing written.
//
// "non-blocking question with an address sends nothing" pins the other side
// of (a): assumed answers arrive by the dozen and must not notify.
// "--project is used as given" pins that a named folder is not replaced by
// its main checkout.
func TestRunInbox(t *testing.T) {
	blockingAdd := []string{"add", "--kind", "env_var", "--title", "Set STRIPE_KEY", "--block"}

	cases := []struct {
		name  string
		setup func(t *testing.T, f *inboxFixture)
		// args is the command line; argsFn replaces it when the row needs
		// the fixture's folders.
		args       []string
		argsFn     func(f *inboxFixture) []string
		wantExit   int
		wantStdout []string
		wantStderr []string
		// emptyStderr asserts nothing at all was printed to stderr.
		emptyStderr bool
		wantSends   int
		// wantFiles is the item files expected in the project's main
		// checkout after the command.
		wantFiles []string
		check     func(t *testing.T, f *inboxFixture)
	}{
		// --- add: notification ---
		{
			name:        "(a) blocking item with an address sends once",
			setup:       func(t *testing.T, f *inboxFixture) { f.ntfyURL = inboxTestURL },
			args:        []string{"add", "--kind", "env_var", "--title", "Set STRIPE_KEY", "--body", "The deploy needs it.", "--task", "T4.3", "--block"},
			wantExit:    0,
			wantStdout:  []string{"Added inbox item aaaaaa."},
			emptyStderr: true,
			wantSends:   1,
			wantFiles:   []string{"aaaaaa.json"},
			check: func(t *testing.T, f *inboxFixture) {
				got := f.sends[0]
				want := inboxSend{url: inboxTestURL, msg: notify.NtfyMessage{
					Title:    "afk: Set STRIPE_KEY",
					Body:     "The deploy needs it.\n\nafk inbox show aaaaaa (T4.3)",
					Priority: 4,
				}}
				if got != want {
					t.Errorf("send = %+v, want %+v", got, want)
				}
				if len(f.configRoots) != 1 || f.configRoots[0] != f.project {
					t.Errorf("afk.toml read from %v, want only %s", f.configRoots, f.project)
				}
				it := readInboxItem(t, f.project, "aaaaaa")
				if !it.Blocking || it.Kind != "env_var" || it.Task != "T4.3" || it.Body != "The deploy needs it." {
					t.Errorf("item = %+v", it)
				}
				if it.CreatedAt != "2026-10-11T09:01:00Z" {
					t.Errorf("created_at = %q", it.CreatedAt)
				}
			},
		},
		{
			name:        "(b) no address sends nothing",
			args:        blockingAdd,
			wantExit:    0,
			wantStdout:  []string{"Added inbox item aaaaaa."},
			emptyStderr: true,
			wantSends:   0,
			wantFiles:   []string{"aaaaaa.json"},
		},
		{
			name: "(c) send error still exits 0",
			setup: func(t *testing.T, f *inboxFixture) {
				f.ntfyURL = inboxTestURL
				f.sendErr = errors.New("could not reach https://ntfy.example/afk-topic: connection refused")
			},
			args:       blockingAdd,
			wantExit:   0,
			wantStdout: []string{"Added inbox item aaaaaa."},
			wantStderr: []string{"the item was added, but the notification could not be sent", "connection refused"},
			wantSends:  1,
			wantFiles:  []string{"aaaaaa.json"},
		},
		{
			name: "(d) afk.toml error still exits 0",
			setup: func(t *testing.T, f *inboxFixture) {
				f.configErr = errors.New("afk.toml: [notify] ntfy_url must be a web address")
			},
			args:       blockingAdd,
			wantExit:   0,
			wantStdout: []string{"Added inbox item aaaaaa."},
			wantStderr: []string{"the item was added, but afk.toml could not be read, so no notification was sent", "ntfy_url must be a web address"},
			wantSends:  0,
			wantFiles:  []string{"aaaaaa.json"},
		},
		{
			name: "(e) from a linked worktree the item lands in the main checkout",
			setup: func(t *testing.T, f *inboxFixture) {
				f.ntfyURL = inboxTestURL
				worktree := t.TempDir()
				f.env.cwd = worktree
				// The fixture's gitCommonDir already answers with the main
				// checkout's .git whatever folder it is asked about, which
				// is what git does inside a linked worktree.
			},
			args:       blockingAdd,
			wantExit:   0,
			wantStdout: []string{"Added inbox item aaaaaa."},
			wantSends:  1,
			wantFiles:  []string{"aaaaaa.json"},
			check: func(t *testing.T, f *inboxFixture) {
				if _, err := os.Stat(filepath.Join(f.env.cwd, ".afk")); !os.IsNotExist(err) {
					t.Errorf("the worktree got a .afk folder (stat err = %v); nothing may be written there", err)
				}
				if len(f.configRoots) != 1 || f.configRoots[0] != f.project {
					t.Errorf("afk.toml read from %v, want the main checkout %s", f.configRoots, f.project)
				}
			},
		},
		{
			name:       "(f) neither block flag",
			setup:      func(t *testing.T, f *inboxFixture) { f.ntfyURL = inboxTestURL },
			args:       []string{"add", "--kind", "env_var", "--title", "x"},
			wantExit:   2,
			wantStderr: []string{"say whether this item stops the task: add --block (the task waits for the owner) or --no-block (the task carries on).", "Nothing was added"},
		},
		{
			name:       "(f) both block flags",
			setup:      func(t *testing.T, f *inboxFixture) { f.ntfyURL = inboxTestURL },
			args:       []string{"add", "--kind", "env_var", "--title", "x", "--block", "--no-block"},
			wantExit:   2,
			wantStderr: []string{"say whether this item stops the task"},
		},
		{
			name:        "non-blocking question with an address sends nothing",
			setup:       func(t *testing.T, f *inboxFixture) { f.ntfyURL = inboxTestURL },
			args:        []string{"add", "--kind", "question", "--title", "Which queue?", "--assumed", "sidekiq", "--no-block"},
			wantExit:    0,
			wantStdout:  []string{"Added inbox item aaaaaa."},
			emptyStderr: true,
			wantSends:   0,
			wantFiles:   []string{"aaaaaa.json"},
		},
		{
			name:      "non-blocking env_var with an address sends at priority 3",
			setup:     func(t *testing.T, f *inboxFixture) { f.ntfyURL = inboxTestURL },
			args:      []string{"add", "--kind", "env_var", "--title", "Set SENTRY_DSN", "--no-block"},
			wantExit:  0,
			wantSends: 1,
			wantFiles: []string{"aaaaaa.json"},
			check: func(t *testing.T, f *inboxFixture) {
				if got := f.sends[0].msg; got.Priority != 3 || got.Body != "afk inbox show aaaaaa" {
					t.Errorf("message = %+v", got)
				}
			},
		},

		// --- add: where and what ---
		{
			name: "--project is used as given",
			setup: func(t *testing.T, f *inboxFixture) {
				f.env.cwd = t.TempDir()
			},
			argsFn: func(f *inboxFixture) []string {
				other := filepath.Join(f.env.cwd, "named")
				return []string{"add", "--kind", "notice", "--title", "x", "--no-block", "--project", other}
			},
			wantExit: 0,
			check: func(t *testing.T, f *inboxFixture) {
				named := filepath.Join(f.env.cwd, "named")
				if got := inboxItemFiles(t, named); len(got) != 1 {
					t.Errorf("item files under --project folder = %v, want one", got)
				}
				if len(f.configRoots) != 1 || f.configRoots[0] != named {
					t.Errorf("afk.toml read from %v, want %s", f.configRoots, named)
				}
			},
		},
		{
			name: "--project before the subcommand word",
			setup: func(t *testing.T, f *inboxFixture) {
				f.env.cwd = t.TempDir()
				f.env.gitCommonDir = func(string) string { return "" }
			},
			argsFn: func(f *inboxFixture) []string {
				return []string{"--project", f.project, "add", "--kind", "notice", "--title", "x", "--no-block"}
			},
			wantExit:  0,
			wantFiles: []string{"aaaaaa.json"},
		},
		{
			name: "outside a git repo the working folder is the project",
			setup: func(t *testing.T, f *inboxFixture) {
				f.env.gitCommonDir = func(string) string { return "" }
				f.env.repoRoot = func(string) string { return "" }
			},
			args:      []string{"add", "--kind", "notice", "--title", "x", "--no-block"},
			wantExit:  0,
			wantFiles: []string{"aaaaaa.json"},
			check: func(t *testing.T, f *inboxFixture) {
				want := filepath.Join(f.project, ".afk", "*", "status.json")
				if len(f.globs) != 1 || f.globs[0] != want {
					t.Errorf("glob patterns = %v, want %s", f.globs, want)
				}
			},
		},
		{
			name: "no project and no working directory",
			setup: func(t *testing.T, f *inboxFixture) {
				f.env.cwd = ""
			},
			args:       []string{"add", "--kind", "notice", "--title", "x", "--no-block"},
			wantExit:   2,
			wantStderr: []string{"cannot tell which project to use"},
		},
		{
			name:      "flags between and after nothing, --option twice",
			args:      []string{"add", "--no-block", "--kind", "question", "--option", "sidekiq", "--title", "Which queue?", "--option", "solid queue", "--recommended", "sidekiq", "--assumed", "sidekiq"},
			wantExit:  0,
			wantFiles: []string{"aaaaaa.json"},
			check: func(t *testing.T, f *inboxFixture) {
				it := readInboxItem(t, f.project, "aaaaaa")
				if len(it.Options) != 2 || it.Options[0] != "sidekiq" || it.Options[1] != "solid queue" {
					t.Errorf("options = %q", it.Options)
				}
				if it.Recommended != "sidekiq" || it.Assumed != "sidekiq" || it.Blocking {
					t.Errorf("item = %+v", it)
				}
			},
		},
		{
			name: "session inferred from the one status.json in the worktree",
			setup: func(t *testing.T, f *inboxFixture) {
				worktree := t.TempDir()
				f.env.cwd = filepath.Join(worktree, "sub")
				f.env.repoRoot = func(string) string { return worktree }
				f.statuses = []string{filepath.Join(worktree, ".afk", "demo", "status.json")}
			},
			args:      []string{"add", "--kind", "notice", "--title", "x", "--no-block"},
			wantExit:  0,
			wantFiles: []string{"aaaaaa.json"},
			check: func(t *testing.T, f *inboxFixture) {
				if got := readInboxItem(t, f.project, "aaaaaa").Session; got != "demo" {
					t.Errorf("session = %q, want demo", got)
				}
				// The worktree's top folder, not the subfolder the command
				// ran in and not the main checkout.
				want := filepath.Join(filepath.Dir(f.env.cwd), ".afk", "*", "status.json")
				if len(f.globs) != 1 || f.globs[0] != want {
					t.Errorf("glob patterns = %v, want %s", f.globs, want)
				}
			},
		},
		{
			name: "--session overrides the inferred one",
			setup: func(t *testing.T, f *inboxFixture) {
				f.statuses = []string{filepath.Join(f.project, ".afk", "demo", "status.json")}
			},
			args:      []string{"add", "--kind", "notice", "--title", "x", "--no-block", "--session", "other-one"},
			wantExit:  0,
			wantFiles: []string{"aaaaaa.json"},
			check: func(t *testing.T, f *inboxFixture) {
				if got := readInboxItem(t, f.project, "aaaaaa").Session; got != "other-one" {
					t.Errorf("session = %q, want other-one", got)
				}
			},
		},
		{
			name: "two status files mean no session",
			setup: func(t *testing.T, f *inboxFixture) {
				f.statuses = []string{
					filepath.Join(f.project, ".afk", "one", "status.json"),
					filepath.Join(f.project, ".afk", "two", "status.json"),
				}
			},
			args:      []string{"add", "--kind", "notice", "--title", "x", "--no-block"},
			wantExit:  0,
			wantFiles: []string{"aaaaaa.json"},
			check: func(t *testing.T, f *inboxFixture) {
				if got := readInboxItem(t, f.project, "aaaaaa").Session; got != "" {
					t.Errorf("session = %q, want empty", got)
				}
			},
		},
		{
			name: "a status folder that is not a session name is not passed on",
			setup: func(t *testing.T, f *inboxFixture) {
				f.statuses = []string{filepath.Join(f.project, ".afk", "Not A Session", "status.json")}
			},
			args:      []string{"add", "--kind", "notice", "--title", "x", "--no-block"},
			wantExit:  0,
			wantFiles: []string{"aaaaaa.json"},
			check: func(t *testing.T, f *inboxFixture) {
				if got := readInboxItem(t, f.project, "aaaaaa").Session; got != "" {
					t.Errorf("session = %q, want empty", got)
				}
			},
		},
		{
			name: "a glob error means no session",
			setup: func(t *testing.T, f *inboxFixture) {
				f.env.glob = func(string) ([]string, error) { return nil, filepath.ErrBadPattern }
			},
			args:      []string{"add", "--kind", "notice", "--title", "x", "--no-block"},
			wantExit:  0,
			wantFiles: []string{"aaaaaa.json"},
		},

		// --- add: written wrongly ---
		{
			name:       "add with a positional word",
			args:       []string{"add", "--kind", "notice", "--title", "x", "--no-block", "stray"},
			wantExit:   2,
			wantStderr: []string{`"stray" is not part of this command`},
		},
		{
			name:       "add without --kind",
			args:       []string{"add", "--title", "x", "--block"},
			wantExit:   2,
			wantStderr: []string{"afk inbox add: the item needs a kind. Add --kind"},
		},
		{
			name:       "add without --title",
			args:       []string{"add", "--kind", "notice", "--block"},
			wantExit:   2,
			wantStderr: []string{"afk inbox add: the item needs a title. Add --title"},
		},
		{
			name:       "add with an unknown kind",
			setup:      func(t *testing.T, f *inboxFixture) { f.ntfyURL = inboxTestURL },
			args:       []string{"add", "--kind", "nonsense", "--title", "x", "--block"},
			wantExit:   2,
			wantStderr: []string{`afk inbox add: "nonsense" is not a kind of inbox item`},
		},
		{
			name:       "add with --assumed on a blocking item",
			args:       []string{"add", "--kind", "notice", "--title", "x", "--block", "--assumed", "y"},
			wantExit:   2,
			wantStderr: []string{"afk inbox add: an assumed answer means the task carried on"},
		},
		{
			name:       "add with an unknown flag",
			args:       []string{"add", "--kind", "notice", "--title", "x", "--block", "--json"},
			wantExit:   2,
			wantStderr: []string{"flag provided but not defined"},
		},
		{
			name: "add when the folder cannot be written",
			setup: func(t *testing.T, f *inboxFixture) {
				f.ntfyURL = inboxTestURL
				// .afk as a plain file makes MkdirAll fail for any user,
				// root included.
				if err := os.WriteFile(filepath.Join(f.project, ".afk"), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			args:       blockingAdd,
			wantExit:   1,
			wantStderr: []string{"afk inbox add: could not write the item", "Nothing was added"},
		},

		// --- list ---
		{
			name:        "list of an empty inbox",
			args:        nil,
			wantExit:    0,
			wantStdout:  []string{"The inbox is empty."},
			emptyStderr: true,
		},
		{
			name: "list order and markers",
			setup: func(t *testing.T, f *inboxFixture) {
				f.mustRun("add", "--kind", "notice", "--title", "Idea: dark mode", "--no-block")                     // aaaaaa, open
				f.mustRun("add", "--kind", "verification", "--title", "Check the invoice PDF", "--no-block")         // bbbbbb, answered below
				f.mustRun("add", "--kind", "external_action", "--title", "Approve DNS", "--block", "--task", "T4.3") // cccccc, blocking
				f.mustRun("add", "--kind", "env_var", "--title", "Set STRIPE_KEY", "--block")                        // dddddd, blocking
				f.mustRun("answer", "bbbbbb", "looks", "fine")
			},
			args:        nil,
			wantExit:    0,
			emptyStderr: true,
			wantFiles:   []string{"aaaaaa.json", "bbbbbb.json", "cccccc.json", "dddddd.json"},
			check: func(t *testing.T, f *inboxFixture) {
				want := "" +
					"! cccccc  external_action  T4.3  Approve DNS\n" +
					"! dddddd  env_var                Set STRIPE_KEY\n" +
					"· aaaaaa  notice                 Idea: dark mode\n" +
					"✓ bbbbbb  verification           Check the invoice PDF\n"
				if got := f.stdout.String(); got != want {
					t.Errorf("list =\n%s\nwant\n%s", got, want)
				}
			},
		},
		{
			name: "list with no task ids has no task column",
			setup: func(t *testing.T, f *inboxFixture) {
				f.mustRun("add", "--kind", "notice", "--title", "Idea", "--no-block")
			},
			args:      []string{},
			wantExit:  0,
			wantFiles: []string{"aaaaaa.json"},
			check: func(t *testing.T, f *inboxFixture) {
				if got, want := f.stdout.String(), "· aaaaaa  notice  Idea\n"; got != want {
					t.Errorf("list = %q, want %q", got, want)
				}
			},
		},
		{
			name: "list with an unreadable file",
			setup: func(t *testing.T, f *inboxFixture) {
				f.mustRun("add", "--kind", "notice", "--title", "Idea", "--no-block")
				if err := os.WriteFile(filepath.Join(inbox.Dir(f.project), "zzzzzz.json"), []byte("{not json"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			args:       nil,
			wantExit:   0,
			wantStdout: []string{"· aaaaaa  notice  Idea\n", "1 inbox file could not be read: zzzzzz.json\n"},
			wantFiles:  []string{"aaaaaa.json", "zzzzzz.json"},
		},
		{
			name: "list with --project and nothing else",
			setup: func(t *testing.T, f *inboxFixture) {
				f.mustRun("add", "--kind", "notice", "--title", "Idea", "--no-block")
				f.env.cwd = t.TempDir()
				f.env.gitCommonDir = func(string) string { return "" }
			},
			argsFn:     func(f *inboxFixture) []string { return []string{"--project", f.project} },
			wantExit:   0,
			wantStdout: []string{"· aaaaaa  notice  Idea"},
			wantFiles:  []string{"aaaaaa.json"},
		},

		// --- show ---
		{
			name: "show an item with options",
			setup: func(t *testing.T, f *inboxFixture) {
				f.statuses = []string{filepath.Join(f.project, ".afk", "demo", "status.json")}
				f.mustRun("add", "--kind", "question", "--title", "Which queue?", "--body", "Two would work.\nPick one.",
					"--option", "sidekiq", "--option", "solid queue", "--recommended", "solid queue", "--task", "T2.1", "--block")
			},
			args:        []string{"show", "aaaaaa"},
			wantExit:    0,
			emptyStderr: true,
			wantFiles:   []string{"aaaaaa.json"},
			check: func(t *testing.T, f *inboxFixture) {
				want := "" +
					"id: aaaaaa\n" +
					"kind: question\n" +
					"blocking: yes (the task is waiting for this)\n" +
					"task: T2.1\n" +
					"session: demo\n" +
					"created: 2026-10-11T09:01:00Z\n" +
					"title: Which queue?\n" +
					"body:\n  Two would work.\n  Pick one.\n" +
					"options:\n  1. sidekiq\n  2. solid queue (recommended)\n" +
					"\nTo answer: afk inbox answer aaaaaa <number or text>\n"
				if got := f.stdout.String(); got != want {
					t.Errorf("show =\n%s\nwant\n%s", got, want)
				}
			},
		},
		{
			name: "show an answered item, flag between the words",
			setup: func(t *testing.T, f *inboxFixture) {
				f.mustRun("add", "--kind", "question", "--title", "Which queue?", "--assumed", "sidekiq", "--no-block")
				f.mustRun("answer", "aaaaaa", "sidekiq is fine")
				f.env.cwd = t.TempDir()
				f.env.gitCommonDir = func(string) string { return "" }
			},
			argsFn:    func(f *inboxFixture) []string { return []string{"show", "--project", f.project, "aaaaaa"} },
			wantExit:  0,
			wantFiles: []string{"aaaaaa.json"},
			check: func(t *testing.T, f *inboxFixture) {
				want := "" +
					"id: aaaaaa\n" +
					"kind: question\n" +
					"blocking: no (the task carried on)\n" +
					"created: 2026-10-11T09:01:00Z\n" +
					"title: Which queue?\n" +
					"assumed: sidekiq\n" +
					"answer: sidekiq is fine\n" +
					"answered at: 2026-10-11T09:02:00Z\n" +
					"answered via: cli\n"
				if got := f.stdout.String(); got != want {
					t.Errorf("show =\n%s\nwant\n%s", got, want)
				}
			},
		},
		{
			name: "show an open item with no options, flag after the words",
			setup: func(t *testing.T, f *inboxFixture) {
				f.mustRun("add", "--kind", "notice", "--title", "Idea", "--no-block")
				f.env.cwd = t.TempDir()
				f.env.gitCommonDir = func(string) string { return "" }
			},
			argsFn:     func(f *inboxFixture) []string { return []string{"show", "aaaaaa", "--project", f.project} },
			wantExit:   0,
			wantStdout: []string{"title: Idea\n", "\nTo answer: afk inbox answer aaaaaa <text>\n"},
			wantFiles:  []string{"aaaaaa.json"},
		},
		{
			name:       "show an unknown id",
			args:       []string{"show", "zzzzzz"},
			wantExit:   1,
			wantStderr: []string{"afk inbox show: there is no inbox item zzzzzz. Run `afk inbox` to see the ids."},
		},
		{
			name:       "show something that is not an id",
			args:       []string{"show", "../../etc/passwd"},
			wantExit:   1,
			wantStderr: []string{"there is no inbox item ../../etc/passwd."},
		},
		{
			name: "show an unreadable item",
			setup: func(t *testing.T, f *inboxFixture) {
				if err := os.MkdirAll(inbox.Dir(f.project), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(inbox.Dir(f.project), "zzzzzz.json"), []byte("{not json"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			args:       []string{"show", "zzzzzz"},
			wantExit:   1,
			wantStderr: []string{"afk inbox show: inbox item zzzzzz could not be read"},
			wantFiles:  []string{"zzzzzz.json"},
		},
		{
			name:       "show with no id",
			args:       []string{"show"},
			wantExit:   2,
			wantStderr: []string{"expected one item id, but got 0 word(s)"},
		},
		{
			name:       "show with extra words",
			args:       []string{"show", "aaaaaa", "bbbbbb"},
			wantExit:   2,
			wantStderr: []string{"expected one item id, but got 2 word(s)"},
		},

		// --- answer ---
		{
			name: "answer by option number",
			setup: func(t *testing.T, f *inboxFixture) {
				f.mustRun("add", "--kind", "question", "--title", "Which queue?", "--option", "sidekiq", "--option", "solid queue", "--block")
			},
			args:        []string{"answer", "aaaaaa", "2"},
			wantExit:    0,
			wantStdout:  []string{"Answered aaaaaa: solid queue\n"},
			emptyStderr: true,
			wantFiles:   []string{"aaaaaa.json"},
			check: func(t *testing.T, f *inboxFixture) {
				it := readInboxItem(t, f.project, "aaaaaa")
				if it.Answer != "solid queue" || it.AnsweredVia != "cli" || it.AnsweredAt != "2026-10-11T09:02:00Z" {
					t.Errorf("item = %+v", it)
				}
			},
		},
		{
			name: "answer by text, words joined, flag in the middle",
			setup: func(t *testing.T, f *inboxFixture) {
				f.mustRun("add", "--kind", "env_var", "--title", "Set STRIPE_KEY", "--block")
				f.env.cwd = t.TempDir()
				f.env.gitCommonDir = func(string) string { return "" }
			},
			argsFn: func(f *inboxFixture) []string {
				return []string{"answer", "aaaaaa", "done,", "--project", f.project, "it is", "set"}
			},
			wantExit:   0,
			wantStdout: []string{"Answered aaaaaa: done, it is set\n"},
			wantFiles:  []string{"aaaaaa.json"},
		},
		{
			name: "answer twice keeps the first",
			setup: func(t *testing.T, f *inboxFixture) {
				f.mustRun("add", "--kind", "env_var", "--title", "Set STRIPE_KEY", "--block")
				f.mustRun("answer", "aaaaaa", "done")
			},
			args:       []string{"answer", "aaaaaa", "changed my mind"},
			wantExit:   1,
			wantStderr: []string{"afk inbox answer: aaaaaa was already answered: done", "The first answer is kept and nothing changed."},
			wantFiles:  []string{"aaaaaa.json"},
			check: func(t *testing.T, f *inboxFixture) {
				if got := readInboxItem(t, f.project, "aaaaaa").Answer; got != "done" {
					t.Errorf("answer = %q, want done", got)
				}
			},
		},
		{
			name:       "answer an unknown id",
			args:       []string{"answer", "zzzzzz", "ok"},
			wantExit:   1,
			wantStderr: []string{"afk inbox answer: there is no inbox item zzzzzz. Run `afk inbox` to see the ids."},
		},
		{
			name: "answer with no text",
			setup: func(t *testing.T, f *inboxFixture) {
				f.mustRun("add", "--kind", "env_var", "--title", "Set STRIPE_KEY", "--block")
			},
			args:       []string{"answer", "aaaaaa"},
			wantExit:   2,
			wantStderr: []string{"the answer is missing"},
			wantFiles:  []string{"aaaaaa.json"},
			check: func(t *testing.T, f *inboxFixture) {
				if !readInboxItem(t, f.project, "aaaaaa").Open() {
					t.Error("the item was answered by a command with no answer")
				}
			},
		},
		{
			name: "answer with blank text",
			setup: func(t *testing.T, f *inboxFixture) {
				f.mustRun("add", "--kind", "env_var", "--title", "Set STRIPE_KEY", "--block")
			},
			args:       []string{"answer", "aaaaaa", "  ", " "},
			wantExit:   2,
			wantStderr: []string{"the answer is empty"},
			wantFiles:  []string{"aaaaaa.json"},
		},
		{
			name:       "answer with no id",
			args:       []string{"answer"},
			wantExit:   2,
			wantStderr: []string{"say which item to answer"},
		},

		// --- routing ---
		{
			name:       "unknown subcommand",
			args:       []string{"bogus"},
			wantExit:   2,
			wantStderr: []string{`afk inbox: unknown subcommand "bogus"`, "Usage:"},
		},
		{
			name:       "delete is not a subcommand",
			args:       []string{"delete", "aaaaaa"},
			wantExit:   2,
			wantStderr: []string{`unknown subcommand "delete"`},
		},
		{
			name:       "--help",
			args:       []string{"--help"},
			wantExit:   0,
			wantStderr: []string{"afk inbox — things that need", "afk inbox add --kind KIND", "external_action", "ntfy_url", "Exit codes:"},
		},
		{
			name:       "-h",
			args:       []string{"-h"},
			wantExit:   0,
			wantStderr: []string{"Usage:"},
		},
		{
			name:       "help",
			args:       []string{"help"},
			wantExit:   0,
			wantStderr: []string{"Usage:"},
		},
		{
			name:       "add --help",
			args:       []string{"add", "--help"},
			wantExit:   0,
			wantStderr: []string{"Usage:"},
		},
		{
			name:       "an add flag before the subcommand word",
			args:       []string{"--kind", "notice", "add", "--title", "x", "--no-block"},
			wantExit:   2,
			wantStderr: []string{"flag provided but not defined"},
		},
	}

	for _, tt := range cases {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			f := newInboxFixture(t)
			if tt.setup != nil {
				tt.setup(t, f)
			}
			// Setup may have added items and sent for them; only the
			// command under test is counted.
			f.sends, f.configRoots, f.globs = nil, nil, nil

			args := tt.args
			if tt.argsFn != nil {
				args = tt.argsFn(f)
			}
			code := f.run(args...)
			if code != tt.wantExit {
				t.Errorf("exit = %d, want %d\nstdout: %s\nstderr: %s", code, tt.wantExit, f.stdout.String(), f.stderr.String())
			}
			wantContains(t, "stdout", f.stdout.String(), tt.wantStdout)
			wantContains(t, "stderr", f.stderr.String(), tt.wantStderr)
			if tt.emptyStderr && f.stderr.Len() != 0 {
				t.Errorf("stderr should be empty:\n%s", f.stderr.String())
			}
			if tt.wantExit != 0 && f.stdout.Len() != 0 {
				t.Errorf("a failed command printed to stdout:\n%s", f.stdout.String())
			}
			if len(f.sends) != tt.wantSends {
				t.Errorf("notifications sent = %d, want %d", len(f.sends), tt.wantSends)
			}
			got := inboxItemFiles(t, f.project)
			if strings.Join(got, ",") != strings.Join(tt.wantFiles, ",") {
				t.Errorf("item files in the main checkout = %v, want %v", got, tt.wantFiles)
			}
			if tt.check != nil && !t.Failed() {
				tt.check(t, f)
			}
		})
	}
}

// TestInboxNotification is the "should this item notify, and with what
// message" table.
//
// Load-bearing rows: "no address" (silence even for the loudest item, since
// most projects have no afk.toml), "non-blocking question" (never notifies
// even with an address) and "body and task" (the exact message shape: body,
// blank line, the show command, the task in brackets). The priorities
// themselves are notify.NtfyPriority's table and are tested there; the rows
// here only prove this function passes kind and blocking through unchanged.
func TestInboxNotification(t *testing.T) {
	cases := []struct {
		name     string
		item     inbox.Item
		url      string
		wantSend bool
		want     notify.NtfyMessage
	}{
		{
			name:     "no address",
			item:     inbox.Item{ID: "aaaaaa", Kind: "failure", Blocking: true, Title: "CI failed twice"},
			url:      "",
			wantSend: false,
		},
		{
			name:     "non-blocking question",
			item:     inbox.Item{ID: "aaaaaa", Kind: "question", Title: "Which queue?", Assumed: "sidekiq"},
			url:      inboxTestURL,
			wantSend: false,
		},
		{
			name:     "non-blocking notice",
			item:     inbox.Item{ID: "aaaaaa", Kind: "notice", Title: "Idea"},
			url:      inboxTestURL,
			wantSend: false,
		},
		{
			name:     "blocking question",
			item:     inbox.Item{ID: "aaaaaa", Kind: "question", Blocking: true, Title: "Which queue?"},
			url:      inboxTestURL,
			wantSend: true,
			want:     notify.NtfyMessage{Title: "afk: Which queue?", Body: "afk inbox show aaaaaa", Priority: 4},
		},
		{
			name:     "blocking failure is the loudest",
			item:     inbox.Item{ID: "aaaaaa", Kind: "failure", Blocking: true, Title: "CI failed twice"},
			url:      inboxTestURL,
			wantSend: true,
			want:     notify.NtfyMessage{Title: "afk: CI failed twice", Body: "afk inbox show aaaaaa", Priority: 5},
		},
		{
			name:     "non-blocking verification is quiet",
			item:     inbox.Item{ID: "aaaaaa", Kind: "verification", Title: "Check the PDF"},
			url:      inboxTestURL,
			wantSend: true,
			want:     notify.NtfyMessage{Title: "afk: Check the PDF", Body: "afk inbox show aaaaaa", Priority: 2},
		},
		{
			name:     "body and task",
			item:     inbox.Item{ID: "bbbbbb", Kind: "env_var", Blocking: true, Title: "Set STRIPE_KEY", Body: "The deploy needs it.", Task: "T4.3"},
			url:      inboxTestURL,
			wantSend: true,
			want:     notify.NtfyMessage{Title: "afk: Set STRIPE_KEY", Body: "The deploy needs it.\n\nafk inbox show bbbbbb (T4.3)", Priority: 4},
		},
		{
			name:     "task without a body",
			item:     inbox.Item{ID: "bbbbbb", Kind: "external_action", Title: "Approve DNS", Task: "T4.2a"},
			url:      inboxTestURL,
			wantSend: true,
			want:     notify.NtfyMessage{Title: "afk: Approve DNS", Body: "afk inbox show bbbbbb (T4.2a)", Priority: 3},
		},
	}
	for _, tt := range cases {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			got, send := inboxNotification(tt.item, tt.url)
			if send != tt.wantSend {
				t.Fatalf("send = %v, want %v", send, tt.wantSend)
			}
			if got != tt.want {
				t.Errorf("message = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestInboxItemLink pins the link `afk inbox add` puts in its notification.
//
// Load-bearing rows:
//   - "config could not be read", "no public address" and "no project is this
//     folder" are the three ways to have no link, and each must be an empty
//     string and nothing else: the notification then goes out as it always
//     did.
//   - "the name is the configured one": the page serves a project under its
//     name in the machine config, so a link built from the folder's name
//     would open "no such project".
//   - "two entries for one checkout": the daemon keeps the first, so the link
//     must use the first.
//   - "an address but no token": the page is off, and a link would open
//     nothing.
//
// No link ever holds the token or any query string.
func TestInboxItemLink(t *testing.T) {
	const token = "tok-3f9a1c7e5b2d4a68"
	on := daemonconfig.Web{Token: token, PublicURL: "https://afk.example"}
	projects := []daemonconfig.Project{
		{Name: "blog", Path: "/p/blog"},
		{Name: "my app", Path: "/p/app-folder"},
		{Name: "again", Path: "/wt/app-session"},
	}
	// sameDir stands in for the main-checkout lookup: /wt/app-session is a
	// worktree of /p/app-folder.
	sameDir := func(configured, root string) bool {
		if configured == "/wt/app-session" {
			configured = "/p/app-folder"
		}
		return configured == root
	}
	cases := []struct {
		name   string
		cfg    daemonconfig.Config
		cfgErr error
		root   string
		want   string
	}{
		{name: "the name is the configured one", cfg: daemonconfig.Config{Web: on, Projects: projects},
			root: "/p/app-folder", want: "https://afk.example/p/my%20app/inbox/abc234"},
		{name: "two entries for one checkout", cfg: daemonconfig.Config{Web: on, Projects: projects[1:]},
			root: "/p/app-folder", want: "https://afk.example/p/my%20app/inbox/abc234"},
		{name: "found through a worktree's path", cfg: daemonconfig.Config{Web: on, Projects: projects[2:]},
			root: "/p/app-folder", want: "https://afk.example/p/again/inbox/abc234"},
		{name: "no project is this folder", cfg: daemonconfig.Config{Web: on, Projects: projects}, root: "/p/elsewhere"},
		{name: "no projects at all", cfg: daemonconfig.Config{Web: on}, root: "/p/app-folder"},
		{name: "config could not be read", cfg: daemonconfig.Config{Web: on, Projects: projects},
			cfgErr: errors.New("config.toml: not valid TOML"), root: "/p/app-folder"},
		{name: "no public address", cfg: daemonconfig.Config{Web: daemonconfig.Web{Token: token}, Projects: projects}, root: "/p/app-folder"},
		{name: "an address but no token", cfg: daemonconfig.Config{Web: daemonconfig.Web{PublicURL: "https://afk.example"}, Projects: projects}, root: "/p/app-folder"},
	}
	for _, tt := range cases {
		t.Run(strings.ReplaceAll(tt.name, " ", "_"), func(t *testing.T) {
			got := inboxItemLink(tt.cfg, tt.cfgErr, tt.root, "abc234", sameDir)
			if got != tt.want {
				t.Errorf("link = %q, want %q", got, tt.want)
			}
			if strings.Contains(got, token) || strings.ContainsAny(got, "?#") {
				t.Errorf("link %q holds the token or a query", got)
			}
		})
	}
}

// TestRunInboxWritesTheFourteenKeys reads the file `afk inbox add` wrote as
// raw JSON. Every other test reads items back through the store, which would
// not notice a key going missing; the web page and the daemon will read the
// file directly and rely on all fourteen.
func TestRunInboxWritesTheFourteenKeys(t *testing.T) {
	f := newInboxFixture(t)
	f.mustRun("add", "--kind", "notice", "--title", "Idea", "--no-block")

	data, err := os.ReadFile(filepath.Join(inbox.Dir(f.project), "aaaaaa.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	keys := []string{"id", "created_at", "task", "session", "kind", "blocking", "title", "body",
		"options", "recommended", "assumed", "answer", "answered_at", "answered_via"}
	if len(raw) != len(keys) {
		t.Errorf("file has %d keys, want %d:\n%s", len(raw), len(keys), data)
	}
	for _, k := range keys {
		if _, ok := raw[k]; !ok {
			t.Errorf("file is missing key %q:\n%s", k, data)
		}
	}
	if got := string(raw["options"]); got != "[]" {
		t.Errorf("options = %s, want []", got)
	}
}

// TestRunInboxRealConfigAndSender wires the real afk.toml reader and the real
// ntfy sender through runInbox against a local test server. The table test
// fakes both, so this is the only test that would catch the command passing
// the wrong folder to the reader or the wrong address to the sender.
//
// Load-bearing: exactly one POST with Priority 4 for a blocking env_var item,
// and no request at all when the project has no afk.toml.
func TestRunInboxRealConfigAndSender(t *testing.T) {
	type request struct {
		method, path, priority, title, body, click string
	}
	var (
		mu       sync.Mutex
		requests []request
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, request{r.Method, r.URL.Path, r.Header.Get("Priority"), r.Header.Get("Title"), string(body), r.Header.Get("Click")})
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wire := func(f *inboxFixture) {
		f.env.loadConfig = projectconfig.Load
		f.env.send = func(ctx context.Context, url string, m notify.NtfyMessage) error {
			return notify.Ntfy{URL: url, Client: srv.Client()}.Send(ctx, m)
		}
	}
	args := []string{"add", "--kind", "env_var", "--title", "Set STRIPE_KEY", "--body", "The deploy needs it.", "--block"}

	t.Run("afk.toml_with_an_address_posts_once", func(t *testing.T) {
		f := newInboxFixture(t)
		wire(f)
		toml := "plan = \"docs/PLAN.md\"\n\n[notify]\nntfy_url = \"" + srv.URL + "/afk-test\"\n"
		if err := os.WriteFile(filepath.Join(f.project, "afk.toml"), []byte(toml), 0o644); err != nil {
			t.Fatal(err)
		}
		if code := f.run(args...); code != 0 {
			t.Fatalf("exit = %d, stderr: %s", code, f.stderr.String())
		}
		if f.stderr.Len() != 0 {
			t.Errorf("stderr should be empty:\n%s", f.stderr.String())
		}
		mu.Lock()
		defer mu.Unlock()
		if len(requests) != 1 {
			t.Fatalf("requests = %d, want exactly 1: %+v", len(requests), requests)
		}
		// No machine config, so no Click header: the last field is empty.
		want := request{"POST", "/afk-test", "4", "afk: Set STRIPE_KEY", "The deploy needs it.\n\nafk inbox show aaaaaa", ""}
		if requests[0] != want {
			t.Errorf("request = %+v, want %+v", requests[0], want)
		}
	})

	// The link reaches ntfy as its Click header when the machine config has
	// a web page with a public address and lists this project. The project
	// is listed by a worktree's path on purpose: the daemon serves it from
	// the main checkout under the configured name, so the match has to go
	// through the same main-checkout lookup (the fixture's fake git says
	// every folder belongs to the project's checkout).
	t.Run("machine_config_names_the_project_so_the_link_is_sent", func(t *testing.T) {
		mu.Lock()
		requests = nil
		mu.Unlock()

		f := newInboxFixture(t)
		wire(f)
		f.machineErr = nil
		f.machine = daemonconfig.Config{
			Web: daemonconfig.Web{Token: "tok-3f9a1c7e5b2d4a68", PublicURL: "https://afk.example"},
			Projects: []daemonconfig.Project{
				{Name: "my app", Path: filepath.Join(t.TempDir(), "a-session-worktree")},
			},
		}
		toml := "[notify]\nntfy_url = \"" + srv.URL + "/afk-test\"\n"
		if err := os.WriteFile(filepath.Join(f.project, "afk.toml"), []byte(toml), 0o644); err != nil {
			t.Fatal(err)
		}
		if code := f.run(args...); code != 0 {
			t.Fatalf("exit = %d, stderr: %s", code, f.stderr.String())
		}
		if f.stderr.Len() != 0 {
			t.Errorf("stderr should be empty:\n%s", f.stderr.String())
		}
		if got := f.stdout.String(); got != "Added inbox item aaaaaa.\n" {
			t.Errorf("stdout = %q, want only the added line", got)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(requests) != 1 {
			t.Fatalf("requests = %d, want exactly 1: %+v", len(requests), requests)
		}
		want := request{"POST", "/afk-test", "4", "afk: Set STRIPE_KEY", "The deploy needs it.\n\nafk inbox show aaaaaa",
			"https://afk.example/p/my%20app/inbox/aaaaaa"}
		if requests[0] != want {
			t.Errorf("request = %+v, want %+v", requests[0], want)
		}
	})

	// A machine config that cannot be read changes nothing the owner sees:
	// same exit, same output, same notification, only no link.
	t.Run("broken_machine_config_is_silent", func(t *testing.T) {
		mu.Lock()
		requests = nil
		mu.Unlock()

		f := newInboxFixture(t)
		wire(f)
		f.machineErr = errors.New("config.toml: web.bind is not host:port")
		toml := "[notify]\nntfy_url = \"" + srv.URL + "/afk-test\"\n"
		if err := os.WriteFile(filepath.Join(f.project, "afk.toml"), []byte(toml), 0o644); err != nil {
			t.Fatal(err)
		}
		if code := f.run(args...); code != 0 {
			t.Fatalf("exit = %d, stderr: %s", code, f.stderr.String())
		}
		if f.stderr.Len() != 0 {
			t.Errorf("stderr should be empty:\n%s", f.stderr.String())
		}
		if got := f.stdout.String(); got != "Added inbox item aaaaaa.\n" {
			t.Errorf("stdout = %q, want only the added line", got)
		}
		mu.Lock()
		defer mu.Unlock()
		want := request{"POST", "/afk-test", "4", "afk: Set STRIPE_KEY", "The deploy needs it.\n\nafk inbox show aaaaaa", ""}
		if len(requests) != 1 || requests[0] != want {
			t.Errorf("requests = %+v, want exactly %+v", requests, want)
		}
	})

	t.Run("no_afk.toml_posts_nothing", func(t *testing.T) {
		mu.Lock()
		requests = nil
		mu.Unlock()

		f := newInboxFixture(t)
		wire(f)
		if code := f.run(args...); code != 0 {
			t.Fatalf("exit = %d, stderr: %s", code, f.stderr.String())
		}
		if f.stderr.Len() != 0 {
			t.Errorf("stderr should be empty:\n%s", f.stderr.String())
		}
		if got := inboxItemFiles(t, f.project); len(got) != 1 {
			t.Errorf("item files = %v, want one", got)
		}
		// With nothing to send there is no link to build, so the machine
		// config is not read at all.
		if f.machineLoads != 0 {
			t.Errorf("the machine config was read %d times for an item that sends nothing, want never", f.machineLoads)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(requests) != 0 {
			t.Errorf("requests = %+v, want none", requests)
		}
	})

	t.Run("server_gone_still_exits_0", func(t *testing.T) {
		gone := httptest.NewServer(http.NotFoundHandler())
		url := gone.URL
		gone.Close()

		f := newInboxFixture(t)
		f.env.loadConfig = projectconfig.Load
		f.env.send = func(ctx context.Context, url string, m notify.NtfyMessage) error {
			return notify.Ntfy{URL: url}.Send(ctx, m)
		}
		toml := "[notify]\nntfy_url = \"" + url + "/afk-test\"\n"
		if err := os.WriteFile(filepath.Join(f.project, "afk.toml"), []byte(toml), 0o644); err != nil {
			t.Fatal(err)
		}
		if code := f.run(args...); code != 0 {
			t.Fatalf("exit = %d, stderr: %s", code, f.stderr.String())
		}
		if !strings.Contains(f.stderr.String(), "the item was added, but the notification could not be sent") {
			t.Errorf("stderr missing the warning:\n%s", f.stderr.String())
		}
		if got := inboxItemFiles(t, f.project); len(got) != 1 {
			t.Errorf("item files = %v, want one", got)
		}
	})
}
