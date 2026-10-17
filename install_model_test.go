package main

import (
	"errors"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/dphaener/afk/internal/daemonconfig"
	"github.com/dphaener/afk/internal/install"
	"github.com/dphaener/afk/internal/pluginstall"
	"github.com/dphaener/afk/internal/provider"
)

// updateInstallGolden rewrites the install screens' golden files instead of
// comparing: `go test -run TestInstallGolden . -update`. The diff of
// testdata/install is the review of a layout change, as in internal/ui.
var updateInstallGolden = flag.Bool("update", false, "rewrite golden files under testdata/install")

// assertInstallGolden compares an ANSI-stripped frame with
// testdata/install/<name>.txt, so the file holds what a person sees.
func assertInstallGolden(t *testing.T, name, frame string) {
	t.Helper()
	path := filepath.Join("testdata", "install", name+".txt")
	got := strings.ReplaceAll(ansi.Strip(frame), "\r\n", "\n") + "\n"
	if *updateInstallGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("writing golden %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden %s: %v (run with -update to create it)", path, err)
	}
	if string(want) != got {
		t.Errorf("frame differs from %s (run with -update to accept):\n--- want\n%s\n--- got\n%s", path, want, got)
	}
}

const fakeHome = "/home/u"

func freshInstallFacts() install.Facts {
	return install.Facts{
		OS: "linux", Distro: "ubuntu", DistroVersion: "24.04", Arch: "amd64", Virt: install.VirtVM,
		PackageManager: "apt-get",
		Tools:          []install.Tool{{Name: "git"}, {Name: "gh"}, {Name: "jq"}, {Name: "ruby"}},
		AgentLogin:     install.No, Unattended: provider.UnattendedNotSet, AgentPlugin: install.No,
		BinOnPath: true, BinHasAfk: true, BinIsThis: true,
		GitHubLogin: install.No, GitUsesGitHub: install.No, WebPassword: install.No,
		Service: install.ServiceFacts{Installed: install.No, Enabled: install.No, Running: install.No, Linger: install.No},
	}
}

func doneInstallFacts() install.Facts {
	f := freshInstallFacts()
	for i := range f.Tools {
		f.Tools[i].Found, f.Tools[i].Path = true, "/usr/bin/"+f.Tools[i].Name
	}
	cli, _ := provider.CLI("")
	f.AgentCLI = install.Tool{Name: cli, Found: true, Path: fakeHome + "/.local/bin/" + cli}
	f.AgentLogin, f.Unattended, f.AgentPlugin = install.Yes, provider.UnattendedSet, install.Yes
	f.GitHubLogin, f.GitUsesGitHub = install.Yes, install.Yes
	f.GitName, f.GitEmail = "Ada Lovelace", "ada@example.com"
	f.ProjectLocation, f.ProjectLocationSet = fakeHome+"/code", true
	f.WebPassword = install.Yes
	f.Service = install.ServiceFacts{Installed: install.Yes, Enabled: install.Yes, Running: install.Yes, Linger: install.Yes}
	return f
}

// fakeInstall is the machine the runner tests drive. Each command and write
// is logged as one line; effects change the facts the next probe returns,
// which is how a step "works". A step with no effect leaves the machine as
// it was, which is how a command that exits 0 but does nothing looks.
type fakeInstall struct {
	mu      sync.Mutex
	facts   install.Facts
	probes  int
	log     []string
	files   map[string]string
	dirs    map[string]bool
	vars    map[string]string
	fail    map[string]error
	outputs map[string]string
	effects map[string]func(*install.Facts)
	// goos is the system the runner believes it is on; empty is linux.
	goos string
}

func (f *fakeInstall) record(line string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, line)
	if err := f.fail[line]; err != nil {
		return err
	}
	if fx := f.effects[line]; fx != nil {
		fx(&f.facts)
	}
	return nil
}

type installFileInfo struct {
	name string
	dir  bool
}

func (i installFileInfo) Name() string       { return i.name }
func (i installFileInfo) Size() int64        { return 0 }
func (i installFileInfo) Mode() fs.FileMode  { return 0o755 }
func (i installFileInfo) ModTime() time.Time { return time.Time{} }
func (i installFileInfo) IsDir() bool        { return i.dir }
func (i installFileInfo) Sys() any           { return nil }

func (f *fakeInstall) env() installEnv {
	getenv := func(k string) string {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.vars[k]
	}
	readFile := func(p string) ([]byte, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if s, ok := f.files[p]; ok {
			return []byte(s), nil
		}
		return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
	}
	write := func(kind string) func(string, []byte) error {
		return func(p string, data []byte) error {
			if err := f.record(kind + " " + p); err != nil {
				return err
			}
			f.mu.Lock()
			f.files[p] = string(data)
			f.mu.Unlock()
			return nil
		}
	}
	goos := f.goos
	if goos == "" {
		goos = "linux"
	}
	cfgDeps := daemonconfig.Deps{
		Getenv:   func(k string) string { return map[string]string{"XDG_CONFIG_HOME": fakeHome + "/.config"}[k] },
		HomeDir:  func() (string, error) { return fakeHome, nil },
		ReadFile: readFile,
	}
	return installEnv{
		isTerminal: func() bool { return true },
		probe: func() install.Facts {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.probes++
			// A copy, so the model never shares the fake's slices.
			c := f.facts
			c.Tools = append([]install.Tool(nil), f.facts.Tools...)
			return c
		},
		handover: func(argv []string, done func(error) tea.Msg) tea.Cmd {
			return func() tea.Msg { return done(f.record("handover " + strings.Join(argv, " "))) }
		},
		run: func(argv []string) ([]byte, error) {
			line := "run " + strings.Join(argv, " ")
			if err := f.record(line); err != nil {
				return nil, err
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			out, ok := f.outputs[strings.Join(argv, " ")]
			if !ok && strings.HasPrefix(line, "run gh api") {
				return nil, errors.New("exit status 1")
			}
			return []byte(out), nil
		},
		executable: func() (string, error) { return "/fake/afk", nil },
		home:       fakeHome,
		goos:       goos,
		uid:        func() int { return 501 },
		getenv:     getenv,
		setenv: func(k, v string) error {
			err := f.record("setenv " + k + "=" + v)
			f.mu.Lock()
			f.vars[k] = v
			f.mu.Unlock()
			return err
		},
		stat: func(p string) (fs.FileInfo, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.dirs[p] {
				return installFileInfo{name: filepath.Base(p), dir: true}, nil
			}
			if _, ok := f.files[p]; ok {
				return installFileInfo{name: filepath.Base(p)}, nil
			}
			return nil, &fs.PathError{Op: "stat", Path: p, Err: fs.ErrNotExist}
		},
		mkdirAll: func(p string, _ fs.FileMode) error {
			if err := f.record("mkdir " + p); err != nil {
				return err
			}
			f.mu.Lock()
			f.dirs[p] = true
			f.mu.Unlock()
			return nil
		},
		readFile:        readFile,
		writeText:       write("write"),
		writeUnit:       write("unit"),
		writeUnattended: func() error { return f.record("unattended") },
		profilePath:     fakeHome + "/.config/afk/profile.toml",
		configPath:      daemonconfig.Path(cfgDeps),
		loadConfig:      func() (daemonconfig.Config, error) { return daemonconfig.Load(cfgDeps) },
		hostIP:          func() string { return "10.0.0.5" },
		checkReport: func() string {
			return "afk daemon is running.\nNo usage reading yet. The first one comes once the daemon runs."
		},
	}
}

func installArgv(t *testing.T, get func(string) ([]string, error)) string {
	t.Helper()
	argv, err := get("")
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(argv, " ")
}

// newFakeInstall is a fresh Ubuntu VM where every step's command works.
func newFakeInstall(t *testing.T) *fakeInstall {
	cli, _ := provider.CLI("")
	return &fakeInstall{
		facts: freshInstallFacts(),
		files: map[string]string{},
		dirs:  map[string]bool{fakeHome + "/.local/bin": true},
		vars:  map[string]string{"PATH": "/usr/bin"},
		fail:  map[string]error{},
		outputs: map[string]string{
			"gh api user --jq .name":  "Ada Lovelace",
			"gh api user --jq .email": "ada@example.com",
		},
		effects: map[string]func(*install.Facts){
			"handover sudo apt-get install -y git gh jq ruby": func(f *install.Facts) {
				for i := range f.Tools {
					f.Tools[i].Found, f.Tools[i].Path = true, "/usr/bin/"+f.Tools[i].Name
				}
			},
			"handover " + installArgv(t, provider.InstallCommand): func(f *install.Facts) {
				f.AgentCLI = install.Tool{Name: cli, Found: true, Path: fakeHome + "/.local/bin/" + cli}
			},
			"handover " + installArgv(t, provider.LoginCommand): func(f *install.Facts) { f.AgentLogin = install.Yes },
			"unattended": func(f *install.Facts) { f.Unattended = provider.UnattendedSet },
			"handover /fake/afk plugin install --target " + string(pluginstall.Claude): func(f *install.Facts) { f.AgentPlugin = install.Yes },
			"handover gh auth login":                                  func(f *install.Facts) { f.GitHubLogin = install.Yes },
			"run gh auth setup-git":                                   func(f *install.Facts) { f.GitUsesGitHub = install.Yes },
			"run git config --global user.name Ada Lovelace":          func(f *install.Facts) { f.GitName = "Ada Lovelace" },
			"run git config --global user.email ada@example.com":      func(f *install.Facts) { f.GitEmail = "ada@example.com" },
			"write " + fakeHome + "/.config/afk/profile.toml":         func(f *install.Facts) { f.ProjectLocationSet, f.ProjectLocation = true, fakeHome+"/code" },
			"handover /fake/afk web password":                         func(f *install.Facts) { f.WebPassword = install.Yes },
			"unit " + fakeHome + "/.config/systemd/user/afkd.service": func(f *install.Facts) { f.Service.Installed = install.Yes },
			"handover systemctl --user enable --now afkd":             func(f *install.Facts) { f.Service.Enabled, f.Service.Running = install.Yes, install.Yes },
			"handover loginctl enable-linger":                         func(f *install.Facts) { f.Service.Linger = install.Yes },
			"handover brew install git gh jq ruby": func(f *install.Facts) {
				for i := range f.Tools {
					f.Tools[i].Found, f.Tools[i].Path = true, "/opt/homebrew/bin/"+f.Tools[i].Name
				}
			},
			"handover launchctl bootstrap gui/501 " + macPlist: func(f *install.Facts) {
				f.Service.Installed, f.Service.Enabled, f.Service.Running = install.Yes, install.Yes, install.Yes
			},
		},
	}
}

// runInstallCmd runs one command and returns what it produced. A command
// that does not answer at once is a timer (huh's cursor blink) and is
// dropped: the runner's own commands always answer at once against the fake.
func runInstallCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	select {
	case msg := <-ch:
		return expandInstallMsg(msg)
	case <-time.After(30 * time.Millisecond):
		return nil
	}
}

var teaCmdType = reflect.TypeOf((tea.Cmd)(nil))

func expandInstallMsg(msg tea.Msg) []tea.Msg {
	switch msg := msg.(type) {
	case nil, tea.QuitMsg:
		return nil
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range msg {
			out = append(out, runInstallCmd(c)...)
		}
		return out
	}
	// tea.Sequence's message type is unexported; it is a []tea.Cmd.
	if v := reflect.ValueOf(msg); v.Kind() == reflect.Slice && v.Type().Elem() == teaCmdType {
		var out []tea.Msg
		for i := 0; i < v.Len(); i++ {
			out = append(out, runInstallCmd(v.Index(i).Interface().(tea.Cmd))...)
		}
		return out
	}
	return []tea.Msg{msg}
}

// drive feeds msgs to the model one by one. After each one it runs whatever
// Update returned, and what that returned, until the model waits for input,
// so a key is only pressed once the screen it is meant for is showing.
func drive(t *testing.T, m *installModel, msgs ...tea.Msg) {
	t.Helper()
	n := 0
	for _, key := range msgs {
		queue := []tea.Msg{key}
		for len(queue) > 0 {
			if n++; n > 5000 {
				t.Fatal("the model never settled")
			}
			msg := queue[0]
			queue = queue[1:]
			_, cmd := m.Update(msg)
			queue = append(queue, runInstallCmd(cmd)...)
		}
	}
}

func startInstall(t *testing.T, f *fakeInstall, yes bool, width int) *installModel {
	t.Helper()
	m := newInstallModel(f.env(), yes)
	drive(t, m, tea.WindowSizeMsg{Width: width, Height: 40})
	drive(t, m, runInstallCmd(m.Init())...)
	return m
}

var (
	keyEnter = tea.KeyPressMsg{Code: tea.KeyEnter}
	keyDown  = tea.KeyPressMsg{Code: tea.KeyDown}
	keyCtrlC = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
)

func keyText(s string) []tea.Msg {
	var out []tea.Msg
	for _, r := range s {
		out = append(out, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return out
}

func keyRune(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

func freshRunLog(t *testing.T) []string {
	return []string{
		"handover sudo apt-get update",
		"handover sudo apt-get install -y git gh jq ruby",
		"handover " + installArgv(t, provider.InstallCommand),
		"setenv PATH=" + fakeHome + "/.local/bin:/usr/bin",
		"handover " + installArgv(t, provider.LoginCommand),
		"unattended",
		"handover /fake/afk plugin install --target " + string(pluginstall.Claude),
		"handover gh auth login",
		"run gh auth setup-git",
		"run gh api user --jq .name",
		"run gh api user --jq .email",
		"run gh api user --jq .id",
		"run gh api user --jq .login",
		"run git config --global user.name Ada Lovelace",
		"run git config --global user.email ada@example.com",
		"mkdir " + fakeHome + "/code",
		"write " + fakeHome + "/.config/afk/profile.toml",
		"handover /fake/afk web password",
		"mkdir " + fakeHome + "/.config/afk",
		"write " + fakeHome + "/.config/afk/config.toml",
		"mkdir " + fakeHome + "/.config/systemd/user",
		"unit " + fakeHome + "/.config/systemd/user/afkd.service",
		"handover systemctl --user daemon-reload",
		"handover systemctl --user enable --now afkd",
		"handover loginctl enable-linger",
	}
}

// macPlist is where the runner writes afk's launch agent for fakeHome.
const macPlist = fakeHome + "/Library/LaunchAgents/" + install.LaunchAgentLabel + ".plist"

// macDoneFacts is a Mac with Homebrew where every step but the ones a row
// changes is done. A Mac is always a personal computer, so every macOS row
// types the safety phrase first (macPhrase); --yes never answers it.
func macDoneFacts() install.Facts {
	f := doneInstallFacts()
	f.OS, f.Distro, f.DistroVersion, f.Arch, f.Virt = "darwin", "", "", "arm64", install.VirtNone
	f.PackageManager = "brew"
	for i := range f.Tools {
		f.Tools[i].Path = "/opt/homebrew/bin/" + f.Tools[i].Name
	}
	f.Service.Linger = install.Unknown
	return f
}

// macSetup makes the fake a Mac (macDoneFacts, goos darwin) and lets the
// row change the facts.
func macSetup(edit func(*install.Facts)) func(*fakeInstall) {
	return func(f *fakeInstall) {
		f.goos = "darwin"
		f.facts = macDoneFacts()
		if edit != nil {
			edit(&f.facts)
		}
	}
}

// macServiceOff is a Mac whose agent is not installed or loaded.
func macServiceOff(f *install.Facts) {
	f.Service.Installed, f.Service.Enabled, f.Service.Running = install.No, install.No, install.No
}

// macWantPlist is the plist the runner must write for macDoneFacts with the
// daemon's log in stateDir.
func macWantPlist(stateDir string) string {
	return string(install.RenderLaunchAgent(install.LaunchAgent{
		Program: fakeHome + "/bin/afk",
		PATH:    install.ServicePATH(macDoneFacts(), fakeHome),
		LogPath: stateDir + "/daemon.log",
	}))
}

// macPhrase answers the safety screen a Mac always shows.
func macPhrase(more ...tea.Msg) []tea.Msg {
	return append(append(keyText(riskPhrase), keyEnter), more...)
}

func macFreshServiceLog(stateDir string) []string {
	return []string{
		"mkdir " + fakeHome + "/Library/LaunchAgents",
		"mkdir " + stateDir,
		"unit " + macPlist,
		"handover launchctl bootstrap gui/501 " + macPlist,
	}
}

// TestInstallRunner drives whole runs through Update against the fake
// machine. The load-bearing rows:
//   - "fresh vm under --yes" pins the order of every command and write, and
//     that the machine is probed again after every step (1 + 10 probes).
//   - "a failed step stops the run": nothing after the failure runs and
//     nothing before it is undone (no removal is ever logged).
//   - "a step that changed nothing": the command exits 0 but the re-probe
//     still says to do, and the run must stop there instead of going on.
//   - "--yes on a personal machine" shows the safety screen and refuses any
//     text but the phrase; --yes never answers it.
//   - The "mac" rows pin the macOS path: brew install with no sudo (and its
//     decline and failure), the launch agent written under ~/Library with its
//     log folder made first and loaded with launchctl bootstrap gui/501,
//     bootout only when the probe found the agent loaded, no write when the
//     file is unchanged, the same rules as Linux for a file afk did not
//     write, the desktop-login advice when bootstrap fails, and the plist
//     bytes (PATH and the log under the state folder, XDG_STATE_HOME
//     honoured). No macOS row may log sudo, systemctl, loginctl or the
//     legacy launchctl load/unload; the loop checks that for every one.
func TestInstallRunner(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(*fakeInstall)
		yes        bool
		keys       []tea.Msg
		wantLog    func(t *testing.T) []string
		wantPhase  installPhase
		wantCode   int
		wantProbes int
		wantScreen []string
		check      func(t *testing.T, f *fakeInstall, m *installModel)
	}{
		{
			name: "fresh vm under --yes runs every step in order", yes: true,
			keys: []tea.Msg{keyEnter}, wantLog: freshRunLog,
			wantPhase: installDone, wantCode: 0, wantProbes: 11,
			wantScreen: []string{"afk is set up.", "http://127.0.0.1:4253", "Plan a new project", "afk daemon is running."},
			check: func(t *testing.T, f *fakeInstall, m *installModel) {
				cfg := f.files[fakeHome+"/.config/afk/config.toml"]
				if !strings.Contains(cfg, `bind = "127.0.0.1:4253"`) || strings.Contains(cfg, "token") {
					t.Errorf("config.toml = %q, want a loopback bind and no token", cfg)
				}
				prof := f.files[fakeHome+"/.config/afk/profile.toml"]
				if !strings.Contains(prof, `topic = "project location"`) || !strings.Contains(prof, `value = "/home/u/code"`) {
					t.Errorf("profile.toml = %q, want the project location", prof)
				}
				unit := f.files[fakeHome+"/.config/systemd/user/afkd.service"]
				if !install.WrittenByAfk([]byte(unit)) || !strings.Contains(unit, "\nEnvironment=PATH=/home/u/bin:/home/u/.local/bin:/usr/bin:") {
					t.Errorf("unit = %q, want afk's marker and a live PATH line", unit)
				}
			},
		},
		{
			name: "everything already done changes nothing", yes: false,
			setup: func(f *fakeInstall) { f.facts = doneInstallFacts() },
			keys:  []tea.Msg{keyEnter}, wantLog: func(*testing.T) []string { return nil },
			wantPhase: installDone, wantCode: 0, wantProbes: 1,
			wantScreen: []string{"Everything is in place. Nothing was changed.", "http://127.0.0.1:4253"},
		},
		{
			name: "a failed step stops the run", yes: true,
			setup: func(f *fakeInstall) {
				f.fail["handover sudo apt-get install -y git gh jq ruby"] = errors.New("exit status 100")
			},
			keys: []tea.Msg{keyEnter},
			wantLog: func(*testing.T) []string {
				return []string{"handover sudo apt-get update", "handover sudo apt-get install -y git gh jq ruby"}
			},
			wantPhase: installFailed, wantCode: 1, wantProbes: 1,
			wantScreen: []string{"Stopped at: Install the tools afk needs", "exit status 100", "The steps already done stay done"},
		},
		{
			name: "a failed later step keeps the earlier ones", yes: true,
			setup: func(f *fakeInstall) {
				f.fail["unattended"] = errors.New("~/.x is not a JSON object, so afk left it alone.")
			},
			keys: []tea.Msg{keyEnter},
			wantLog: func(t *testing.T) []string {
				return freshRunLog(t)[:6]
			},
			wantPhase: installFailed, wantCode: 1, wantProbes: 4,
			wantScreen: []string{"Stopped at: Let the agent run without permission prompts", "not a JSON object"},
		},
		{
			name: "a step that changed nothing is caught by the re-probe", yes: true,
			setup: func(f *fakeInstall) { delete(f.effects, "handover "+installArgv(t, provider.LoginCommand)) },
			keys:  []tea.Msg{keyEnter},
			wantLog: func(t *testing.T) []string {
				return freshRunLog(t)[:5]
			},
			wantPhase: installFailed, wantCode: 1, wantProbes: 4,
			wantScreen: []string{"Stopped at: Sign in to the agent CLI", "is still not done", "not signed in"},
		},
		{
			name: "--yes on a personal machine refuses anything but the phrase", yes: true,
			setup:     func(f *fakeInstall) { f.facts = doneInstallFacts(); f.facts.Desktop = true },
			keys:      append(append([]tea.Msg{keyEnter}, keyText("yes")...), keyEnter),
			wantLog:   func(*testing.T) []string { return nil },
			wantPhase: installSafety, wantCode: 1, wantProbes: 1,
			wantScreen: []string{"Before anything changes", "That is not the phrase", "--yes does not answer this"},
		},
		{
			name: "the typed phrase passes the safety screen", yes: true,
			setup:     func(f *fakeInstall) { f.facts = doneInstallFacts(); f.facts.Desktop = true },
			keys:      append(keyText(riskPhrase), keyEnter),
			wantLog:   func(*testing.T) []string { return nil },
			wantPhase: installDone, wantCode: 0, wantProbes: 1,
			wantScreen: []string{"✓ Check that this machine is a safe place for afk"},
		},
		{
			name: "root is refused with the fix and nothing else", yes: true,
			setup:     func(f *fakeInstall) { f.facts.Root = true },
			keys:      []tea.Msg{keyEnter},
			wantLog:   func(*testing.T) []string { return nil },
			wantPhase: installRefused, wantCode: 1, wantProbes: 1,
			wantScreen: []string{"afk install cannot set up this machine.", "not root", "adduser"},
		},
		{
			name: "saying no to a confirm stops before the step", yes: false,
			keys:      []tea.Msg{keyEnter, keyRune('n')},
			wantLog:   func(*testing.T) []string { return nil },
			wantPhase: installFailed, wantCode: 1, wantProbes: 1,
			wantScreen: []string{"Stopped at: Install the tools afk needs", "You said no"},
		},
		{
			name: "ctrl+c on the safety screen changes nothing", yes: false,
			keys:      []tea.Msg{keyCtrlC},
			wantLog:   func(*testing.T) []string { return nil },
			wantPhase: installFailed, wantCode: 1, wantProbes: 1,
			wantScreen: []string{"You stopped afk install. Nothing was changed."},
		},
		{
			name: "github already signed in only sets up git", yes: true,
			setup:     func(f *fakeInstall) { f.facts = doneInstallFacts(); f.facts.GitUsesGitHub = install.No },
			keys:      []tea.Msg{keyEnter},
			wantLog:   func(*testing.T) []string { return []string{"run gh auth setup-git"} },
			wantPhase: installDone, wantCode: 0, wantProbes: 2,
		},
		{
			name: "a cannot step stops the run with its fix", yes: true,
			setup:     func(f *fakeInstall) { f.facts = doneInstallFacts(); f.facts.BinHasAfk = false },
			keys:      []tea.Msg{keyEnter},
			wantLog:   func(*testing.T) []string { return nil },
			wantPhase: installFailed, wantCode: 1, wantProbes: 1,
			wantScreen: []string{"Stopped at: Start the afk service", "Put afk in ~/bin first"},
		},
		{
			name: "a unit afk did not write is never replaced under --yes", yes: true,
			setup: func(f *fakeInstall) {
				f.facts = doneInstallFacts()
				f.facts.Service.Running = install.No
				f.files[fakeHome+"/.config/systemd/user/afkd.service"] = "[Service]\nExecStart=/opt/mine\n"
			},
			keys:      []tea.Msg{keyEnter},
			wantLog:   func(*testing.T) []string { return nil },
			wantPhase: installFailed, wantCode: 1, wantProbes: 1,
			wantScreen: []string{"afk did not write, and --yes never replaces it", "Move the file aside"},
		},
		{
			name: "a unit afk did not write is kept on the default answer", yes: false,
			setup: func(f *fakeInstall) {
				f.facts = doneInstallFacts()
				f.facts.Service.Running = install.No
				f.files[fakeHome+"/.config/systemd/user/afkd.service"] = "[Service]\nExecStart=/opt/mine\n"
			},
			keys:      []tea.Msg{keyEnter, keyEnter},
			wantLog:   func(*testing.T) []string { return nil },
			wantPhase: installFailed, wantCode: 1, wantProbes: 1,
			wantScreen: []string{"The service file was left as it was."},
		},
		{
			name: "a unit afk did not write is replaced after a yes", yes: false,
			setup: func(f *fakeInstall) {
				f.facts = doneInstallFacts()
				f.facts.Service.Running = install.No
				f.files[fakeHome+"/.config/systemd/user/afkd.service"] = "[Service]\nExecStart=/opt/mine\n"
			},
			keys: []tea.Msg{keyEnter, keyRune('y')},
			wantLog: func(*testing.T) []string {
				return []string{
					"mkdir " + fakeHome + "/.config/systemd/user",
					"unit " + fakeHome + "/.config/systemd/user/afkd.service",
					"handover systemctl --user daemon-reload",
					"handover systemctl --user enable --now afkd",
					"handover loginctl enable-linger",
				}
			},
			wantPhase: installDone, wantCode: 0, wantProbes: 2,
		},
		{
			name: "tailscale address is saved as public_url", yes: false,
			setup: func(f *fakeInstall) {
				f.facts = doneInstallFacts()
				f.facts.WebPassword, f.facts.Tailscale = install.No, true
				f.outputs["tailscale serve --bg 4253"] = "Available within your tailnet:\n\nhttps://afk.tail1234.ts.net/\n|-- / proxy http://127.0.0.1:4253\n"
			},
			// safety, password confirm, pick the third choice, serve confirm.
			keys: []tea.Msg{keyEnter, keyEnter, keyDown, keyDown, keyEnter, keyEnter},
			wantLog: func(*testing.T) []string {
				return []string{
					"handover /fake/afk web password",
					"run tailscale serve --bg 4253",
					"mkdir " + fakeHome + "/.config/afk",
					"write " + fakeHome + "/.config/afk/config.toml",
				}
			},
			wantPhase: installDone, wantCode: 0, wantProbes: 2,
			wantScreen: []string{"https://afk.tail1234.ts.net"},
			check: func(t *testing.T, f *fakeInstall, m *installModel) {
				cfg := f.files[fakeHome+"/.config/afk/config.toml"]
				for _, w := range []string{`bind = "127.0.0.1:4253"`, `public_url = "https://afk.tail1234.ts.net"`} {
					if !strings.Contains(cfg, w) {
						t.Errorf("config.toml = %q, missing %s", cfg, w)
					}
				}
			},
		},
		{
			name: "every address is the second choice", yes: false,
			setup: func(f *fakeInstall) { f.facts = doneInstallFacts(); f.facts.WebPassword = install.No },
			keys:  []tea.Msg{keyEnter, keyEnter, keyDown, keyEnter},
			wantLog: func(*testing.T) []string {
				return []string{"handover /fake/afk web password", "mkdir " + fakeHome + "/.config/afk", "write " + fakeHome + "/.config/afk/config.toml"}
			},
			wantPhase: installDone, wantCode: 0, wantProbes: 2,
			wantScreen: []string{"http://10.0.0.5:4253"},
		},
		{
			name: "git identity form when GitHub knows nothing", yes: true,
			setup: func(f *fakeInstall) {
				f.facts = doneInstallFacts()
				f.facts.GitName, f.facts.GitEmail = "", ""
				f.outputs = map[string]string{}
			},
			keys: []tea.Msg{keyEnter, tea.PasteMsg{Content: "Ada Lovelace"}, keyEnter, tea.PasteMsg{Content: "ada@example.com"}, keyEnter},
			wantLog: func(*testing.T) []string {
				return []string{
					"run gh api user --jq .name", "run gh api user --jq .email", "run gh api user --jq .id", "run gh api user --jq .login",
					"run git config --global user.name Ada Lovelace", "run git config --global user.email ada@example.com",
				}
			},
			wantPhase: installDone, wantCode: 0, wantProbes: 2,
		},
		{
			name: "project location form takes the default", yes: false,
			setup: func(f *fakeInstall) { f.facts = doneInstallFacts(); f.facts.ProjectLocationSet = false },
			keys:  []tea.Msg{keyEnter, keyEnter},
			wantLog: func(*testing.T) []string {
				return []string{"mkdir " + fakeHome + "/code", "write " + fakeHome + "/.config/afk/profile.toml"}
			},
			wantPhase: installDone, wantCode: 0, wantProbes: 2,
		},
		{
			name: "mac brew installs the missing tools after a yes", yes: false,
			setup:     macSetup(func(f *install.Facts) { f.Tools = freshInstallFacts().Tools }),
			keys:      macPhrase(keyEnter),
			wantLog:   func(*testing.T) []string { return []string{"handover brew install git gh jq ruby"} },
			wantPhase: installDone, wantCode: 0, wantProbes: 2,
		},
		{
			name: "mac brew declined stops before brew runs", yes: false,
			setup:     macSetup(func(f *install.Facts) { f.Tools = freshInstallFacts().Tools }),
			keys:      macPhrase(keyRune('n')),
			wantLog:   func(*testing.T) []string { return nil },
			wantPhase: installFailed, wantCode: 1, wantProbes: 1,
			wantScreen: []string{"Stopped at: Install the tools afk needs", "You said no"},
		},
		{
			name: "mac brew install failing stops the run with the todo", yes: true,
			setup: func(f *fakeInstall) {
				macSetup(func(f *install.Facts) { f.Tools = freshInstallFacts().Tools })(f)
				f.fail["handover brew install git gh jq ruby"] = errors.New("exit status 1")
			},
			keys:      macPhrase(),
			wantLog:   func(*testing.T) []string { return []string{"handover brew install git gh jq ruby"} },
			wantPhase: installFailed, wantCode: 1, wantProbes: 1,
			wantScreen: []string{"brew install git gh jq ruby did not finish", "run brew doctor"},
		},
		{
			name: "mac service fresh writes the plist and bootstraps it", yes: true,
			setup:     macSetup(macServiceOff),
			keys:      macPhrase(),
			wantLog:   func(*testing.T) []string { return macFreshServiceLog(fakeHome + "/.local/state/afk") },
			wantPhase: installDone, wantCode: 0, wantProbes: 2,
			check: func(t *testing.T, f *fakeInstall, m *installModel) {
				got := f.files[macPlist]
				if want := macWantPlist(fakeHome + "/.local/state/afk"); got != want {
					t.Errorf("plist:\n%s\nwant:\n%s", got, want)
				}
				if !strings.Contains(got, "<string>/home/u/bin:/home/u/.local/bin:/opt/homebrew/bin:") {
					t.Errorf("plist PATH does not hold the brew folder:\n%s", got)
				}
			},
		},
		{
			name: "mac service confirm names the launchctl command", yes: false,
			setup:     macSetup(macServiceOff),
			keys:      macPhrase(),
			wantLog:   func(*testing.T) []string { return nil },
			wantPhase: installConfirm, wantCode: 1, wantProbes: 1,
			wantScreen: []string{"Install and start the afk service?", "launchctl bootstrap gui/501 " + macPlist, "each time you log in"},
		},
		{
			name: "mac service follows XDG_STATE_HOME for the log", yes: true,
			setup: func(f *fakeInstall) {
				macSetup(macServiceOff)(f)
				f.vars["XDG_STATE_HOME"] = "/x/state"
			},
			keys:      macPhrase(),
			wantLog:   func(*testing.T) []string { return macFreshServiceLog("/x/state/afk") },
			wantPhase: installDone, wantCode: 0, wantProbes: 2,
			check: func(t *testing.T, f *fakeInstall, m *installModel) {
				if got, want := f.files[macPlist], macWantPlist("/x/state/afk"); got != want {
					t.Errorf("plist:\n%s\nwant:\n%s", got, want)
				}
			},
		},
		{
			name: "mac service already loaded with a changed plist boots out first", yes: false,
			setup: func(f *fakeInstall) {
				macSetup(func(f *install.Facts) { f.Service.Running = install.No })(f)
				f.files[macPlist] = string(install.RenderLaunchAgent(install.LaunchAgent{Program: fakeHome + "/bin/afk", PATH: "/usr/bin", LogPath: "/old.log"}))
			},
			keys: macPhrase(keyEnter),
			wantLog: func(*testing.T) []string {
				return []string{
					"mkdir " + fakeHome + "/Library/LaunchAgents",
					"mkdir " + fakeHome + "/.local/state/afk",
					"unit " + macPlist,
					"handover launchctl bootout gui/501/" + install.LaunchAgentLabel,
					"handover launchctl bootstrap gui/501 " + macPlist,
				}
			},
			wantPhase: installDone, wantCode: 0, wantProbes: 2,
			check: func(t *testing.T, f *fakeInstall, m *installModel) {
				if got, want := f.files[macPlist], macWantPlist(fakeHome+"/.local/state/afk"); got != want {
					t.Errorf("plist was not replaced:\n%s", got)
				}
			},
		},
		{
			name: "mac service unchanged plist and not loaded only bootstraps", yes: true,
			setup: func(f *fakeInstall) {
				macSetup(func(f *install.Facts) {
					f.Service.Enabled, f.Service.Running = install.No, install.No
				})(f)
				f.files[macPlist] = macWantPlist(fakeHome + "/.local/state/afk")
			},
			keys:      macPhrase(),
			wantLog:   func(*testing.T) []string { return []string{"handover launchctl bootstrap gui/501 " + macPlist} },
			wantPhase: installDone, wantCode: 0, wantProbes: 2,
		},
		{
			name: "mac plist afk did not write is never replaced under --yes", yes: true,
			setup: func(f *fakeInstall) {
				macSetup(macServiceOff)(f)
				f.files[macPlist] = "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<plist version=\"1.0\"><dict/></plist>\n"
			},
			keys:      macPhrase(),
			wantLog:   func(*testing.T) []string { return nil },
			wantPhase: installFailed, wantCode: 1, wantProbes: 1,
			wantScreen: []string{"service file at " + macPlist + " that afk did not write, and --yes never replaces it"},
		},
		{
			name: "mac plist afk did not write is kept on the default answer", yes: false,
			setup: func(f *fakeInstall) {
				macSetup(macServiceOff)(f)
				f.files[macPlist] = "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<plist version=\"1.0\"><dict/></plist>\n"
			},
			keys:      macPhrase(keyEnter),
			wantLog:   func(*testing.T) []string { return nil },
			wantPhase: installFailed, wantCode: 1, wantProbes: 1,
			wantScreen: []string{"The service file was left as it was."},
		},
		{
			name: "mac plist afk did not write is replaced after a yes", yes: false,
			setup: func(f *fakeInstall) {
				macSetup(macServiceOff)(f)
				f.files[macPlist] = "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<plist version=\"1.0\"><dict/></plist>\n"
			},
			keys:      macPhrase(keyRune('y')),
			wantLog:   func(*testing.T) []string { return macFreshServiceLog(fakeHome + "/.local/state/afk") },
			wantPhase: installDone, wantCode: 0, wantProbes: 2,
		},
		{
			name: "mac bootstrap failing gives the desktop-login advice", yes: true,
			setup: func(f *fakeInstall) {
				macSetup(macServiceOff)(f)
				f.fail["handover launchctl bootstrap gui/501 "+macPlist] = errors.New("exit status 5")
			},
			keys:      macPhrase(),
			wantLog:   func(*testing.T) []string { return macFreshServiceLog(fakeHome + "/.local/state/afk") },
			wantPhase: installFailed, wantCode: 1, wantProbes: 1,
			wantScreen: []string{"Stopped at: Start the afk service", "exit status 5", "from a desktop login on this Mac, not over SSH", "launchctl print gui/501/" + install.LaunchAgentLabel},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeInstall(t)
			if tt.setup != nil {
				tt.setup(f)
			}
			m := startInstall(t, f, tt.yes, 100)
			drive(t, m, tt.keys...)
			if want := tt.wantLog(t); !reflect.DeepEqual(f.log, want) {
				t.Errorf("command log:\n%s\nwant:\n%s", strings.Join(f.log, "\n"), strings.Join(want, "\n"))
			}
			if m.phase != tt.wantPhase {
				t.Errorf("phase = %d, want %d\n%s", m.phase, tt.wantPhase, ansi.Strip(m.render()))
			}
			if m.exitCode != tt.wantCode {
				t.Errorf("exit code = %d, want %d", m.exitCode, tt.wantCode)
			}
			if f.probes != tt.wantProbes {
				t.Errorf("probes = %d, want %d", f.probes, tt.wantProbes)
			}
			screen := ansi.Strip(m.render())
			for _, w := range tt.wantScreen {
				if !strings.Contains(strings.Join(strings.Fields(screen), " "), w) {
					t.Errorf("screen missing %q:\n%s", w, screen)
				}
			}
			for _, l := range f.log {
				if strings.HasPrefix(l, "remove") {
					t.Errorf("the run removed something: %s", l)
				}
			}
			if f.goos == "darwin" {
				for _, l := range f.log {
					for _, bad := range []string{"sudo", "systemctl", "loginctl", "launchctl load", "launchctl unload"} {
						if strings.Contains(l, bad) {
							t.Errorf("a macOS run logged %q: %s", bad, l)
						}
					}
				}
			}
			if tt.check != nil {
				tt.check(t, f, m)
			}
		})
	}
}

// TestInstallRiskAcceptedOnEveryProbe: once the phrase is typed, every later
// probe's facts carry RiskAccepted, so the safety step stays done; without
// it the plan's first to-do would be safety again and the run would stop.
func TestInstallRiskAcceptedOnEveryProbe(t *testing.T) {
	f := newFakeInstall(t)
	f.facts = doneInstallFacts()
	f.facts.Desktop = true
	f.facts.GitUsesGitHub = install.No
	m := startInstall(t, f, true, 100)
	drive(t, m, append(keyText(riskPhrase), keyEnter)...)
	if !m.facts.RiskAccepted || m.phase != installDone {
		t.Fatalf("RiskAccepted = %v, phase = %d", m.facts.RiskAccepted, m.phase)
	}
	if f.probes != 2 {
		t.Errorf("probes = %d, want 2", f.probes)
	}
}

// TestInstallGolden renders the screens a person reads at the two widths the
// dashboard is checked at.
func TestInstallGolden(t *testing.T) {
	type screen struct {
		name  string
		build func(t *testing.T, width int) *installModel
	}
	screens := []screen{
		{"checklist", func(t *testing.T, w int) *installModel {
			f := newFakeInstall(t)
			f.facts.BinHasAfk = false
			m := newInstallModel(f.env(), false)
			m.width = w
			m.setFacts(f.facts)
			m.working = "Looking at this machine…"
			return m
		}},
		{"safety_personal", func(t *testing.T, w int) *installModel {
			f := newFakeInstall(t)
			f.facts.Desktop = true
			return startInstall(t, f, false, w)
		}},
		{"safety_vm", func(t *testing.T, w int) *installModel {
			return startInstall(t, newFakeInstall(t), false, w)
		}},
		{"failure", func(t *testing.T, w int) *installModel {
			f := newFakeInstall(t)
			f.fail["handover sudo apt-get update"] = errors.New("exit status 100")
			m := startInstall(t, f, true, w)
			drive(t, m, keyEnter)
			return m
		}},
		{"done", func(t *testing.T, w int) *installModel {
			f := newFakeInstall(t)
			f.facts = doneInstallFacts()
			m := startInstall(t, f, false, w)
			drive(t, m, keyEnter)
			return m
		}},
	}
	for _, s := range screens {
		for _, w := range []int{80, 120} {
			name := s.name + "_" + map[int]string{80: "80", 120: "120"}[w]
			t.Run(name, func(t *testing.T) {
				m := s.build(t, w)
				frame := m.render()
				for i, l := range strings.Split(ansi.Strip(frame), "\n") {
					if n := ansi.StringWidth(l); n > w {
						t.Errorf("line %d is %d columns, wider than %d: %q", i+1, n, w, l)
					}
				}
				assertInstallGolden(t, "install_"+name, frame)
			})
		}
	}
}
