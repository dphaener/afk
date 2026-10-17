package main

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/dphaener/afk/internal/daemonconfig"
	"github.com/dphaener/afk/internal/install"
	"github.com/dphaener/afk/internal/pluginstall"
	"github.com/dphaener/afk/internal/profile"
	"github.com/dphaener/afk/internal/provider"
)

// installProjectTopic is the profile topic planfolder reads the project
// location from.
const installProjectTopic = "project location"

// installDefaultLocation is offered, and taken under --yes, when no project
// location is set.
const installDefaultLocation = "~/code"

// The three answers to "who can open the web page". Loopback is first and is
// the --yes answer because it exposes nothing: the page can run agents, so
// reaching it from elsewhere is something the owner must choose.
const (
	installBindLocal     = "local"
	installBindAll       = "all"
	installBindTailscale = "tailscale"
)

// installWebPort is the port of afk's default bind, so the three choices
// never disagree with daemonconfig about it.
func installWebPort() string {
	i := strings.LastIndex(daemonconfig.DefaultWebBind, ":")
	return daemonconfig.DefaultWebBind[i+1:]
}

// runStep does one to-do step. Every outside effect goes through m.env.
func (m *installModel) runStep(s install.Step) tea.Cmd {
	switch s.ID {
	case install.StepPackages:
		return m.stepPackages()
	case install.StepAgentCLI:
		return m.stepAgentCLI()
	case install.StepAgentLogin:
		return m.stepAgentLogin()
	case install.StepUnattended:
		return m.stepUnattended()
	case install.StepPlugin:
		return m.stepPlugin()
	case install.StepGitHubLogin:
		return m.stepGitHubLogin()
	case install.StepGitIdentity:
		return m.stepGitIdentity()
	case install.StepProjectLocation:
		return m.stepProjectLocation()
	case install.StepWebPage:
		return m.stepWebPage()
	case install.StepService:
		return m.stepService()
	}
	// Safety is passed on its own screen before the loop starts, and Check
	// is never a to-do action (install.Next skips it), so reaching here means
	// the plan grew a step this binary has no action for.
	return m.failStep("This version of afk install has no action for this step.", "Do it by hand, or update afk, then run afk install again.")
}

// handoverChain hands the terminal to each command in turn and stops the run
// at the first one that fails. todo is the advice for a failure.
func (m *installModel) handoverChain(cmds [][]string, todo string, then func() tea.Cmd) tea.Cmd {
	if len(cmds) == 0 {
		return then()
	}
	argv := cmds[0]
	return m.handover(argv, func(err error) tea.Cmd {
		if err != nil {
			return m.failStep(fmt.Sprintf("%s did not finish: %v.", strings.Join(argv, " "), err), todo)
		}
		return m.handoverChain(cmds[1:], todo, then)
	})
}

// stepPackages is the only place afk uses sudo, and it never runs sudo
// itself with captured output: the terminal is handed over so sudo asks for
// the password in its own prompt and afk never sees it.
func (m *installModel) stepPackages() tea.Cmd {
	pkgs := install.AptPackages(m.facts)
	list := strings.Join(pkgs, " ")
	if m.facts.PackageManager != "apt-get" {
		return m.failStep("afk install can only install packages with apt-get so far.",
			"Install "+list+" with your system's package manager, then run afk install again.")
	}
	if len(pkgs) == 0 {
		return m.stepDone()
	}
	return m.ask("Install "+strings.Join(pkgs, ", ")+" with sudo apt-get?",
		"afk will run sudo apt-get update, then sudo apt-get install -y "+list+". sudo asks for your password itself; afk never sees it.",
		true, func(yes bool) tea.Cmd {
			if !yes {
				return m.declined()
			}
			return m.handoverChain([][]string{
				{"sudo", "apt-get", "update"},
				append([]string{"sudo", "apt-get", "install", "-y"}, pkgs...),
			}, "Check your network connection and that your user may use sudo.", m.stepDone)
		})
}

func (m *installModel) stepAgentCLI() tea.Cmd {
	argv, err := provider.InstallCommand("")
	if err != nil {
		return m.failStep(err.Error()+".", "")
	}
	return m.ask("Install the agent CLI?",
		"afk will run the installer its maker publishes, as you and not as root: "+strings.Join(argv, " "),
		true, func(yes bool) tea.Cmd {
			if !yes {
				return m.declined()
			}
			return m.handoverChain([][]string{argv}, "Check your network connection.", func() tea.Cmd {
				m.addLocalBin()
				return m.stepDone()
			})
		})
}

// addLocalBin puts ~/.local/bin on this process's PATH when the folder exists
// and is not there. The agent CLI's installer puts the program there, and a
// new PATH line in the shell's startup file only reaches new terminals: the
// re-probe and every later step run in this process, and would otherwise not
// find the program that was just installed.
func (m *installModel) addLocalBin() {
	dir := filepath.Join(m.env.home, ".local", "bin")
	fi, err := m.env.stat(dir)
	if err != nil || !fi.IsDir() {
		return
	}
	path := m.env.getenv("PATH")
	if installPathHas(path, dir) {
		return
	}
	if path == "" {
		m.env.setenv("PATH", dir)
		return
	}
	m.env.setenv("PATH", dir+":"+path)
}

func installPathHas(path, dir string) bool {
	dir = filepath.Clean(dir)
	for _, e := range strings.Split(path, ":") {
		if e != "" && filepath.Clean(e) == dir {
			return true
		}
	}
	return false
}

func (m *installModel) stepAgentLogin() tea.Cmd {
	argv, err := provider.LoginCommand("")
	if err != nil {
		return m.failStep(err.Error()+".", "")
	}
	return m.ask("Sign in to the agent CLI now?",
		"afk hands the terminal to the agent CLI's own sign-in ("+strings.Join(argv, " ")+"). You type your details into it; afk never sees them.",
		true, func(yes bool) tea.Cmd {
			if !yes {
				return m.declined()
			}
			return m.handoverChain([][]string{argv}, "Finish the sign-in, or run "+strings.Join(argv, " ")+" yourself.", m.stepDone)
		})
}

func (m *installModel) stepUnattended() tea.Cmd {
	write := m.env.writeUnattended
	return m.ask("Turn off the agent CLI's permission prompts?",
		"afk will set this in the agent CLI's settings file and keep everything else in it. It is what lets agents work while nobody is watching, and it is why the safety screen came first.",
		true, func(yes bool) tea.Cmd {
			if !yes {
				return m.declined()
			}
			return m.async("Writing the agent CLI's setting…", func() installResult { return installResult{err: write()} },
				func(r installResult) tea.Cmd {
					if r.err != nil {
						return m.failStep(r.err.Error(), "")
					}
					return m.stepDone()
				})
		})
}

// selfCommand is this binary with args. The plugin and the web password are
// done by afk's own commands, run with the terminal handed over, so the
// password is typed into `afk web password`'s no-echo prompt and this
// process never holds it.
func (m *installModel) selfCommand(args ...string) ([]string, error) {
	self, err := m.env.executable()
	if err != nil {
		return nil, fmt.Errorf("afk could not find its own program file: %v", err)
	}
	return append([]string{self}, args...), nil
}

func (m *installModel) stepPlugin() tea.Cmd {
	// pluginstall's target name, not a literal: the plugin target and the
	// provider package are the two places that know the agent's name.
	argv, err := m.selfCommand("plugin", "install", "--target", string(pluginstall.Claude))
	if err != nil {
		return m.failStep(err.Error()+".", "")
	}
	return m.ask("Install the afk plugin into the agent CLI?",
		"afk will run afk plugin install --target "+string(pluginstall.Claude)+".",
		true, func(yes bool) tea.Cmd {
			if !yes {
				return m.declined()
			}
			return m.handoverChain([][]string{argv}, "", m.stepDone)
		})
}

func (m *installModel) stepGitHubLogin() tea.Cmd {
	needLogin := m.facts.GitHubLogin != install.Yes
	question, detail := "Set git up to use your GitHub login?", "afk will run gh auth setup-git, so git pushes with your GitHub login."
	if needLogin {
		question = "Sign in to GitHub now?"
		detail = "afk hands the terminal to gh auth login, then runs gh auth setup-git so git pushes with that login. You sign in through gh; afk never sees your details."
	}
	run := m.env.run
	return m.ask(question, detail, true, func(yes bool) tea.Cmd {
		if !yes {
			return m.declined()
		}
		setup := func() tea.Cmd {
			return m.async("Running gh auth setup-git…", func() installResult {
				_, err := run([]string{"gh", "auth", "setup-git"})
				return installResult{err: err}
			}, func(r installResult) tea.Cmd {
				if r.err != nil {
					return m.failStep(fmt.Sprintf("gh auth setup-git did not finish: %v.", r.err), "Run gh auth setup-git yourself to see what is wrong.")
				}
				return m.stepDone()
			})
		}
		if !needLogin {
			return setup()
		}
		return m.handoverChain([][]string{{"gh", "auth", "login"}}, "Finish the sign-in, or run gh auth login yourself.", setup)
	})
}

// installGitHubUser is what `gh api user` says, for prefilling git's name
// and email.
type installGitHubUser struct{ name, email, id, login string }

// installIdentity picks the name and email to offer: what git already has
// first, then the GitHub account's, then GitHub's private no-reply address,
// which is what GitHub itself suggests for people who keep their email
// private.
func installIdentity(gitName, gitEmail string, gh installGitHubUser) (name, email string) {
	name, email = gitName, gitEmail
	if name == "" {
		name = gh.name
	}
	if email == "" {
		email = gh.email
	}
	if email == "" && gh.id != "" && gh.login != "" {
		email = gh.id + "+" + gh.login + "@users.noreply.github.com"
	}
	return name, email
}

func (m *installModel) stepGitIdentity() tea.Cmd {
	run := m.env.run
	ask := func(field string) string {
		out, err := run([]string{"gh", "api", "user", "--jq", field})
		if err != nil {
			return ""
		}
		v := strings.TrimSpace(string(out))
		if v == "null" {
			return ""
		}
		return v
	}
	return m.async("Asking GitHub for your name and email…", func() installResult {
		return installResult{value: installGitHubUser{name: ask(".name"), email: ask(".email"), id: ask(".id"), login: ask(".login")}}
	}, func(r installResult) tea.Cmd {
		gh, _ := r.value.(installGitHubUser)
		name, email := installIdentity(m.facts.GitName, m.facts.GitEmail, gh)
		if m.yes && name != "" && email != "" {
			return m.writeIdentity(name, email)
		}
		f := huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("Your name for git commits").Value(&name).
				Validate(func(v string) error {
					if strings.TrimSpace(v) == "" {
						return errors.New("give a name")
					}
					return nil
				}),
			huh.NewInput().Title("Your email for git commits").Value(&email).
				Validate(func(v string) error {
					if !strings.Contains(v, "@") {
						return errors.New("give an email address, like you@example.com")
					}
					return nil
				}),
		))
		return m.showForm(f, func() tea.Cmd { return m.writeIdentity(strings.TrimSpace(name), strings.TrimSpace(email)) })
	})
}

func (m *installModel) writeIdentity(name, email string) tea.Cmd {
	run := m.env.run
	return m.async("Saving your git name and email…", func() installResult {
		if _, err := run([]string{"git", "config", "--global", "user.name", name}); err != nil {
			return installResult{err: err}
		}
		_, err := run([]string{"git", "config", "--global", "user.email", email})
		return installResult{err: err}
	}, func(r installResult) tea.Cmd {
		if r.err != nil {
			return m.failStep(fmt.Sprintf("git config did not finish: %v.", r.err), "")
		}
		return m.stepDone()
	})
}

// installExpandLocation turns the typed project location into an absolute
// folder. planfolder refuses a relative location, so one is refused here
// too rather than saved and refused later.
func installExpandLocation(v, home string) (string, error) {
	v = strings.TrimSpace(v)
	switch {
	case v == "":
		return "", errors.New("give a folder, like ~/code")
	case v == "~":
		v = home
	default:
		v = profile.ExpandHome(v, home)
	}
	if !filepath.IsAbs(v) {
		return "", errors.New("give a full path, starting with / or ~/")
	}
	return filepath.Clean(v), nil
}

func (m *installModel) stepProjectLocation() tea.Cmd {
	loc := installDefaultLocation
	home := m.env.home
	if m.yes {
		return m.saveLocation(loc)
	}
	f := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Where should your projects live?").
			Description("afk makes each new project in a folder here. ~ is your home folder.").
			Value(&loc).
			Validate(func(v string) error { _, err := installExpandLocation(v, home); return err }),
	))
	return m.showForm(f, func() tea.Cmd { return m.saveLocation(loc) })
}

func (m *installModel) saveLocation(typed string) tea.Cmd {
	env := m.env
	path, err := installExpandLocation(typed, env.home)
	if err != nil {
		return m.failStep("The project location is not usable: "+err.Error()+".", "")
	}
	return m.async("Saving the project location…", func() installResult {
		return installResult{err: installSaveLocation(env, path)}
	}, func(r installResult) tea.Cmd {
		if r.err != nil {
			return m.failStep(r.err.Error(), "")
		}
		return m.stepDone()
	})
}

func installSaveLocation(env installEnv, path string) error {
	fi, err := env.stat(path)
	switch {
	case err == nil && !fi.IsDir():
		return fmt.Errorf("%s is a file, not a folder. Choose another place", path)
	case errors.Is(err, fs.ErrNotExist):
		if err := env.mkdirAll(path, 0o755); err != nil {
			return fmt.Errorf("afk could not make %s: %v", path, err)
		}
	case err != nil:
		return fmt.Errorf("afk could not look at %s: %v", path, err)
	}
	if env.profilePath == "" {
		return errors.New("afk could not find its config folder. Make sure HOME is set")
	}
	text, err := installReadText(env, env.profilePath)
	if err != nil {
		return err
	}
	newText, _, err := profile.Set(text, profile.Entry{Topic: installProjectTopic, Value: path})
	if err != nil {
		return fmt.Errorf("afk could not add the project location to %s: %v", env.profilePath, err)
	}
	if err := env.writeText(env.profilePath, []byte(newText)); err != nil {
		return fmt.Errorf("afk could not write %s: %v", env.profilePath, err)
	}
	return nil
}

// installReadText reads a settings file; a missing one is empty text, so the
// first edit creates it.
func installReadText(env installEnv, path string) (string, error) {
	data, err := env.readFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("afk could not read %s: %v", path, err)
	}
	return string(data), nil
}

func (m *installModel) stepWebPage() tea.Cmd {
	argv, err := m.selfCommand("web", "password")
	if err != nil {
		return m.failStep(err.Error()+".", "")
	}
	return m.ask("Set the web page password now?",
		"afk runs afk web password, which asks for it twice without showing it and saves only a hash. afk install never reads the password.",
		true, func(yes bool) tea.Cmd {
			if !yes {
				return m.declined()
			}
			return m.handoverChain([][]string{argv}, "Run afk web password yourself to see what is wrong.", m.chooseBind)
		})
}

func (m *installModel) chooseBind() tea.Cmd {
	choice := installBindLocal
	if m.yes {
		return m.saveBind(choice)
	}
	port := installWebPort()
	opts := []huh.Option[string]{
		huh.NewOption("This machine only (127.0.0.1:"+port+")", installBindLocal),
		huh.NewOption("Every address (0.0.0.0:"+port+"), for a Multipass VM you open from your own computer", installBindAll),
	}
	if m.facts.Tailscale {
		opts = append(opts, huh.NewOption("Tailscale: your own devices on your tailnet, over https", installBindTailscale))
	}
	f := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().Title("Who should be able to open the web page?").Options(opts...).Value(&choice),
	))
	return m.showForm(f, func() tea.Cmd { return m.saveBind(choice) })
}

func (m *installModel) saveBind(choice string) tea.Cmd {
	env := m.env
	port := installWebPort()
	switch choice {
	case installBindAll:
		return m.writeWeb([][2]string{{"bind", "0.0.0.0:" + port}})
	case installBindTailscale:
		return m.ask("Share the web page on your tailnet with tailscale serve?",
			"afk will run tailscale serve --bg "+port+". Only devices signed in to your tailnet can reach it, over https; the page itself keeps listening on this machine only.",
			true, func(yes bool) tea.Cmd {
				if !yes {
					return m.declined()
				}
				return m.async("Running tailscale serve…", func() installResult {
					out, err := env.run([]string{"tailscale", "serve", "--bg", port})
					return installResult{out: out, err: err}
				}, func(r installResult) tea.Cmd {
					if r.err != nil {
						return m.failStep(fmt.Sprintf("tailscale serve did not finish: %v.", r.err), "Check that Tailscale is signed in (tailscale status).")
					}
					url := installTailscaleURL(string(r.out))
					if url == "" {
						return m.failStep("tailscale serve did not print an https address.", "Run tailscale serve status to see what it set up.")
					}
					return m.writeWeb([][2]string{{"bind", "127.0.0.1:" + port}, {"public_url", url}})
				})
			})
	}
	return m.writeWeb([][2]string{{"bind", "127.0.0.1:" + port}})
}

// installTailscaleURL is the first https:// address in tailscale serve's
// output, without a trailing slash (config.toml's public_url has none).
func installTailscaleURL(out string) string {
	for _, f := range strings.Fields(out) {
		if strings.HasPrefix(f, "https://") {
			return strings.TrimRight(f, "/.,;)")
		}
	}
	return ""
}

func (m *installModel) writeWeb(kv [][2]string) tea.Cmd {
	env := m.env
	return m.async("Saving the web page settings…", func() installResult {
		return installResult{err: installWriteWeb(env, kv)}
	}, func(r installResult) tea.Cmd {
		if r.err != nil {
			return m.failStep(r.err.Error(), "")
		}
		return m.stepDone()
	})
}

// installWriteWeb is a read-modify-write of config.toml through
// daemonconfig.SetWeb, which only edits the [web] lines it is given and
// refuses a token: the page's password lives in its own file, never here.
func installWriteWeb(env installEnv, kv [][2]string) error {
	if env.configPath == "" {
		return errors.New("afk could not find its config folder. Make sure HOME is set")
	}
	text, err := installReadText(env, env.configPath)
	if err != nil {
		return err
	}
	for _, p := range kv {
		if text, err = daemonconfig.SetWeb(text, p[0], p[1]); err != nil {
			return fmt.Errorf("%s: %v", env.configPath, err)
		}
	}
	if err := env.mkdirAll(filepath.Dir(env.configPath), 0o700); err != nil {
		return fmt.Errorf("afk could not make %s: %v", filepath.Dir(env.configPath), err)
	}
	if err := env.writeText(env.configPath, []byte(text)); err != nil {
		return fmt.Errorf("afk could not write %s: %v", env.configPath, err)
	}
	return nil
}

func (m *installModel) unitPath() string {
	return filepath.Join(m.env.home, ".config", "systemd", "user", "afkd.service")
}

// installUnitShown is how much of a foreign unit file the replace question
// quotes; a unit is a few dozen lines, so this only guards a file that is
// not a unit at all.
const installUnitShown = 40

func (m *installModel) stepService() tea.Cmd {
	if m.env.goos != "linux" {
		return m.failStep("afk install can only start the service on Linux so far.", "Start afk daemon by hand for now (see contrib/README.md).")
	}
	path := m.unitPath()
	want := install.RenderUnit(afkdUnit, install.ServicePATH(m.facts, m.env.home))
	old, err := m.env.readFile(path)
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return m.failStep(fmt.Sprintf("afk could not read %s: %v.", path, err), "")
	}
	commands := [][]string{
		{"systemctl", "--user", "daemon-reload"},
		{"systemctl", "--user", "enable", "--now", "afkd"},
		// Through the handover as well: on some systems enabling linger asks
		// polkit for permission, and that prompt needs the terminal.
		{"loginctl", "enable-linger"},
	}
	start := func() tea.Cmd {
		return m.handoverChain(commands, "Run systemctl --user status afkd to see what is wrong.", m.stepDone)
	}
	write := func() tea.Cmd {
		if exists && string(old) == string(want) {
			return start()
		}
		env := m.env
		return m.async("Writing the service file…", func() installResult {
			if err := env.mkdirAll(filepath.Dir(path), 0o755); err != nil {
				return installResult{err: fmt.Errorf("afk could not make %s: %v", filepath.Dir(path), err)}
			}
			if err := env.writeUnit(path, want); err != nil {
				return installResult{err: fmt.Errorf("afk could not write %s: %v", path, err)}
			}
			return installResult{}
		}, func(r installResult) tea.Cmd {
			if r.err != nil {
				return m.failStep(r.err.Error(), "")
			}
			return start()
		})
	}
	if exists && !install.WrittenByAfk(old) {
		if m.yes {
			return m.failStep("There is already a service file at "+path+" that afk did not write, and --yes never replaces it.",
				"Move the file aside, or run afk install again without --yes to see it and choose.")
		}
		lines := strings.Split(strings.TrimRight(string(old), "\n"), "\n")
		if len(lines) > installUnitShown {
			lines = append(lines[:installUnitShown], "…")
		}
		return m.ask("Replace the service file that afk did not write?",
			"This is the file now at "+path+":\n\n"+strings.Join(lines, "\n"),
			false, func(yes bool) tea.Cmd {
				if !yes {
					return m.failStep("The service file was left as it was.", "Move it aside or edit it yourself.")
				}
				return write()
			})
	}
	return m.ask("Install and start the afk service?",
		"afk will write "+path+", then run systemctl --user daemon-reload, systemctl --user enable --now afkd and loginctl enable-linger, so afk keeps running after you log out.",
		true, func(yes bool) tea.Cmd {
			if !yes {
				return m.declined()
			}
			return write()
		})
}
