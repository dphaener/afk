package install

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/dphaener/afk/internal/daemonconfig"
	"github.com/dphaener/afk/internal/planfolder"
	"github.com/dphaener/afk/internal/profile"
	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/usagestate"
	"github.com/dphaener/afk/internal/webpass"
)

// LaunchAgentLabel is the label of afk's macOS launch agent. It is exported
// so the macOS install step (T11.4) writes the same plist Probe looks for.
const LaunchAgentLabel = "io.github.dphaener.afk"

// defaultTimeout bounds every command Probe runs. A probe that hangs would
// freeze the installer on its first screen, so no command gets to wait.
const defaultTimeout = 10 * time.Second

// Deps is everything Probe reads from the machine. There are only reads and
// command runners here, by construction: Probe cannot write, create or
// remove a file, because no dependency that could is offered to it. That is
// the whole guarantee that checking the machine changes nothing.
type Deps struct {
	GOOS, GOARCH string
	Euid         func() int
	Getenv       func(string) string
	HomeDir      func() (string, error)
	ReadFile     func(string) ([]byte, error)
	Stat         func(string) (fs.FileInfo, error)
	LookPath     func(string) (string, error)
	// Executable is the path of the running binary.
	Executable   func() (string, error)
	EvalSymlinks func(string) (string, error)
	// Run runs one command and returns its stdout. Probe always hands it a
	// context with a deadline.
	Run provider.CmdFunc
	// Timeout bounds each command; zero means 10 seconds.
	Timeout time.Duration
	// Provider names the agent provider; empty means the default.
	Provider string
}

// DefaultDeps wires the real machine. It is the only place in this package
// that names os, os/exec or runtime.
func DefaultDeps() Deps {
	return Deps{
		GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		Euid: os.Geteuid, Getenv: os.Getenv, HomeDir: os.UserHomeDir,
		ReadFile: os.ReadFile, Stat: os.Stat, LookPath: exec.LookPath,
		Executable: os.Executable, EvalSymlinks: filepath.EvalSymlinks,
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output()
		},
	}
}

// prober carries Deps plus whether the last command hit its deadline.
type prober struct {
	d        Deps
	home     string
	timedOut bool
}

// run is the only way Probe starts a command. It gives the command a
// deadline, and records when the deadline was what stopped it.
func (p *prober) run(name string, args ...string) (string, error) {
	t := p.d.Timeout
	if t <= 0 {
		t = defaultTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), t)
	defer cancel()
	p.timedOut = false
	if p.d.Run == nil {
		return "", errors.New("no command runner")
	}
	out, err := p.d.Run(ctx, name, args...)
	if ctx.Err() != nil {
		p.timedOut = true
		if err == nil {
			err = ctx.Err()
		}
	}
	return strings.TrimSpace(string(out)), err
}

// exitedNonZero reports whether err is a command that ran and exited with a
// failure status, as opposed to one that never started or was cut off. Only
// the former is a real "no" answer. It uses an interface rather than
// *exec.ExitError so a test fake can stand in.
func exitedNonZero(err error) bool {
	var e interface{ ExitCode() int }
	return errors.As(err, &e) && e.ExitCode() > 0
}

// yesNo turns a command's outcome into a Check: success is Yes, a non-zero
// exit is No, and anything else (missing command, timeout) is Unknown.
func (p *prober) yesNo(err error) Check {
	switch {
	case err == nil:
		return Yes
	case !p.timedOut && exitedNonZero(err):
		return No
	}
	return Unknown
}

// outputIs reads a command whose output is the answer even when the exit
// status is non-zero. Matching output is Yes, other output is No, and no
// output with an error is Unknown.
func outputIs(out string, want string) Check {
	switch {
	case out == want:
		return Yes
	case out != "":
		return No
	}
	return Unknown
}

// Probe reads the machine into Facts. Every command has a deadline, and a
// command that errors or times out gives Unknown, never No, unless its exit
// status is the documented "no" answer. It writes nothing.
func Probe(d Deps) Facts {
	p := &prober{d: d}
	f := Facts{OS: d.GOOS, Arch: d.GOARCH}
	if d.HomeDir != nil {
		if h, err := d.HomeDir(); err == nil {
			p.home = h
		}
	}
	getenv := d.Getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	linux, darwin := d.GOOS == "linux", d.GOOS == "darwin"
	euid := -1
	if d.Euid != nil {
		euid = d.Euid()
	}
	f.Root = euid == 0

	if linux {
		f.Distro, f.DistroVersion = p.osRelease()
	}
	f.Virt = p.virt(darwin)
	f.Desktop = darwin
	if linux {
		f.Desktop = getenv("XDG_CURRENT_DESKTOP") != "" ||
			getenv("XDG_SESSION_TYPE") == "x11" || getenv("XDG_SESSION_TYPE") == "wayland" ||
			p.exists("/etc/systemd/system/display-manager.service") == Yes
	}

	switch {
	case linux && p.found("apt-get"):
		f.PackageManager = "apt-get"
	case darwin && p.found("brew"):
		f.PackageManager = "brew"
	}
	for _, name := range []string{"git", "gh", "jq", "ruby"} {
		f.Tools = append(f.Tools, p.tool(name))
	}

	p.agent(&f)

	binDir := filepath.Join(p.home, "bin")
	f.BinOnPath = p.home != "" && pathHas(getenv("PATH"), binDir)
	f.BinHasAfk = p.home != "" && p.exists(filepath.Join(binDir, "afk")) == Yes
	f.BinIsThis = p.binIsThis(filepath.Join(binDir, "afk"))

	_, err := p.run("gh", "auth", "status")
	f.GitHubLogin = p.yesNo(err)
	out, err := p.run("git", "config", "--global", "--get-all", "credential.https://github.com.helper")
	switch {
	case strings.Contains(out, "gh auth git-credential"):
		f.GitUsesGitHub = Yes
	case err != nil && (p.timedOut || !exitedNonZero(err)):
		f.GitUsesGitHub = Unknown
	default:
		f.GitUsesGitHub = No
	}
	f.GitName, _ = p.gitConfig("user.name")
	f.GitEmail, _ = p.gitConfig("user.email")

	p.settings(&f, getenv)
	f.Tailscale = p.found("tailscale")
	switch {
	case linux:
		p.linuxService(&f, getenv, euid)
	case darwin:
		p.macService(&f, euid)
	}
	return f
}

func (p *prober) found(name string) bool {
	if p.d.LookPath == nil {
		return false
	}
	_, err := p.d.LookPath(name)
	return err == nil
}

func (p *prober) tool(name string) Tool {
	t := Tool{Name: name}
	if p.d.LookPath != nil {
		if path, err := p.d.LookPath(name); err == nil {
			t.Found, t.Path = true, path
		}
	}
	return t
}

// exists is Yes or No for a path, and Unknown for any error that is not "it
// is not there" (a permission problem tells us nothing).
func (p *prober) exists(path string) Check {
	if p.d.Stat == nil {
		return Unknown
	}
	_, err := p.d.Stat(path)
	switch {
	case err == nil:
		return Yes
	case errors.Is(err, fs.ErrNotExist):
		return No
	}
	return Unknown
}

func (p *prober) gitConfig(key string) (string, error) {
	out, err := p.run("git", "config", "--global", key)
	if err != nil {
		return "", err
	}
	return out, nil
}

// osRelease reads ID and VERSION_ID, stripping quotes. An unreadable file
// gives empty strings.
func (p *prober) osRelease() (id, version string) {
	if p.d.ReadFile == nil {
		return "", ""
	}
	data, err := p.d.ReadFile("/etc/os-release")
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch k {
		case "ID":
			id = v
		case "VERSION_ID":
			version = v
		}
	}
	return id, version
}

// virt asks systemd-detect-virt. It exits non-zero while printing "none", so
// the output is read before the error is looked at.
func (p *prober) virt(darwin bool) Virt {
	if darwin {
		return VirtNone
	}
	if p.d.GOOS != "linux" {
		return VirtUnknown
	}
	vm, _ := p.run("systemd-detect-virt", "--vm")
	if vm != "" && vm != "none" {
		return VirtVM
	}
	if vm != "none" {
		return VirtUnknown
	}
	ct, _ := p.run("systemd-detect-virt", "--container")
	switch {
	case ct == "none":
		return VirtNone
	case ct != "":
		return VirtContainer
	}
	return VirtUnknown
}

// agent fills the facts that depend on the agent CLI. The command name comes
// from the provider package, so no vendor is named here.
func (p *prober) agent(f *Facts) {
	f.AgentLogin, f.AgentPlugin = Unknown, Unknown
	name, err := provider.CLI(p.d.Provider)
	if err == nil {
		f.AgentCLI = p.tool(name)
	}
	f.Unattended, f.UnattendedProblem = provider.ReadUnattendedSetting(p.d.Provider, provider.UnattendedDeps{
		Home: p.home, Euid: p.d.Euid, ReadFile: p.d.ReadFile,
	})
	if err != nil || !f.AgentCLI.Found {
		return
	}
	pr, err := provider.Lookup(p.d.Provider)
	if err != nil {
		return
	}
	st, err := pr.Login(context.Background(), provider.LoginDeps{
		// The caller's context is ignored: p.run builds the deadline itself,
		// so no command can run without one.
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			out, err := p.run(name, args...)
			return []byte(out), err
		},
	})
	switch {
	case err != nil:
		f.AgentLogin = Unknown
	case st.LoggedIn:
		f.AgentLogin = Yes
	default:
		f.AgentLogin = No
	}

	timedOut := false
	ok, _ := pr.Preflight(p.home, func(_, name string, args ...string) (string, error) {
		out, err := p.run(name, args...)
		if p.timedOut {
			timedOut = true
		}
		return out, err
	})
	switch {
	case ok:
		f.AgentPlugin = Yes
	case timedOut:
		f.AgentPlugin = Unknown
	default:
		f.AgentPlugin = No
	}
}

// pathHas reports whether dir is one of the entries of a PATH value.
func pathHas(path, dir string) bool {
	dir = filepath.Clean(dir)
	for _, e := range strings.Split(path, ":") {
		if e != "" && filepath.Clean(e) == dir {
			return true
		}
	}
	return false
}

func (p *prober) binIsThis(afk string) bool {
	if p.d.Executable == nil || p.d.EvalSymlinks == nil {
		return false
	}
	self, err := p.d.Executable()
	if err != nil {
		return false
	}
	a, err := p.d.EvalSymlinks(self)
	if err != nil {
		return false
	}
	b, err := p.d.EvalSymlinks(afk)
	return err == nil && a == b
}

// settings reads the project location, the web password and the web bind.
// All of it goes through read-only dependencies.
func (p *prober) settings(f *Facts, getenv func(string) string) {
	read := p.d.ReadFile
	if read == nil {
		read = func(string) ([]byte, error) { return nil, errors.New("no file reader") }
	}
	homeDir := func() (string, error) {
		if p.home == "" {
			return "", errors.New("home folder unknown")
		}
		return p.home, nil
	}
	pd := profile.Deps{Getenv: getenv, HomeDir: homeDir, ReadFile: read}
	prof, _, found, _, lerr := profile.Load(pd)
	loc := planfolder.LocationFrom(prof, found, lerr, p.home)
	f.ProjectLocation, f.ProjectLocationSet, f.ProjectLocationProblem = loc.Path, loc.Set, loc.Problem

	// Only the read-side fields are set; webpass fills the rest with real
	// functions it never calls on this path.
	_, err := webpass.Read(usagestate.Deps{Getenv: getenv, HomeDir: homeDir, ReadFile: read})
	switch {
	case err == nil:
		f.WebPassword = Yes
	case errors.Is(err, webpass.ErrNoPassword):
		f.WebPassword = No
	default:
		f.WebPassword = Unknown
	}
	cfg, err := daemonconfig.Load(daemonconfig.Deps{Getenv: getenv, HomeDir: homeDir, ReadFile: read})
	if err == nil {
		f.WebBind = cfg.Web.Bind
	}
}

func (p *prober) linuxService(f *Facts, getenv func(string) string, euid int) {
	f.Service.Installed = p.exists(filepath.Join(p.home, ".config", "systemd", "user", "afkd.service"))
	en, _ := p.run("systemctl", "--user", "is-enabled", "afkd")
	f.Service.Enabled = outputIs(en, "enabled")
	act, _ := p.run("systemctl", "--user", "is-active", "afkd")
	f.Service.Running = outputIs(act, "active")
	user := getenv("USER")
	if user == "" && euid >= 0 {
		user = strconv.Itoa(euid)
	}
	ling, _ := p.run("loginctl", "show-user", user, "--property=Linger")
	f.Service.Linger = outputIs(ling, "Linger=yes")
}

// macService reads launchd. The launchctl reading is provisional until T11.4
// checks it against launchd's documentation: `launchctl print` exiting 0 is
// taken to mean the agent is loaded, and "state = running" in its output to
// mean it is running. Linger has no meaning on macOS and stays Unknown.
func (p *prober) macService(f *Facts, euid int) {
	f.Service.Linger = Unknown
	f.Service.Installed = p.exists(filepath.Join(p.home, "Library", "LaunchAgents", LaunchAgentLabel+".plist"))
	out, err := p.run("launchctl", "print", "gui/"+strconv.Itoa(euid)+"/"+LaunchAgentLabel)
	f.Service.Enabled = p.yesNo(err)
	switch {
	case err == nil && strings.Contains(out, "state = running"):
		f.Service.Running = Yes
	case err == nil:
		f.Service.Running = No
	default:
		f.Service.Running = f.Service.Enabled
	}
}
