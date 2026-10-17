package install

import (
	"fmt"
	"strings"

	"github.com/dphaener/afk/internal/provider"
)

// stepTitles holds the title of each step, in install order.
var stepTitles = []struct {
	id    StepID
	title string
}{
	{StepSafety, "Check that this machine is a safe place for afk"},
	{StepPackages, "Install the tools afk needs"},
	{StepAgentCLI, "Install the agent CLI"},
	{StepAgentLogin, "Sign in to the agent CLI"},
	{StepUnattended, "Let the agent run without permission prompts"},
	{StepPlugin, "Install the afk plugin"},
	{StepGitHubLogin, "Sign in to GitHub"},
	{StepGitIdentity, "Set your git name and email"},
	{StepProjectLocation, "Choose where projects live"},
	{StepWebPage, "Set the web page password"},
	{StepService, "Start the afk service"},
	{StepCheck, "Check everything"},
}

// riskOf is computed even when the install cannot go on (root, unsupported
// system), so the screen can still say how risky this machine is. A desktop
// or a Mac wins over virtualisation: a VM on a daily-use laptop's desktop
// session is still the person's own computer.
func riskOf(f Facts) Risk {
	if f.OS == "darwin" || (f.OS == "linux" && f.Desktop) {
		return RiskPersonal
	}
	switch f.Virt {
	case VirtVM, VirtContainer:
		return RiskVM
	case VirtNone:
		return RiskDedicated
	}
	return RiskUnknown
}

// Build turns Facts into the plan. It is pure; see the package doc.
func Build(f Facts) Plan {
	p := Plan{Risk: riskOf(f)}

	// A blanket "cannot" is one message, not twelve different ones: nothing
	// else matters until the person fixes it.
	blanket := ""
	switch {
	case f.OS != "linux" && f.OS != "darwin":
		blanket = "afk install works on Linux and macOS only."
	case f.Root:
		blanket = "Run afk install as your normal user, not root. " +
			"To make one on Linux, run adduser with a name you choose, then sign in as that user."
	}
	if blanket != "" {
		for _, t := range stepTitles {
			reason := "This machine cannot be set up as it is."
			p.Steps = append(p.Steps, Step{ID: t.id, Title: t.title, State: Cannot, Reason: reason, Fix: blanket})
		}
		return p
	}

	add := func(id StepID, state State, reason, fix string) {
		title := ""
		for _, t := range stepTitles {
			if t.id == id {
				title = t.title
			}
		}
		p.Steps = append(p.Steps, Step{ID: id, Title: title, State: state, Reason: reason, Fix: fix})
	}

	// Safety.
	switch p.Risk {
	case RiskDedicated, RiskVM:
		add(StepSafety, Done, "This machine is set aside for afk, so agents running here put nothing else at risk.", "")
	default:
		add(StepSafety, ToDo, "This looks like a computer you use every day. afk runs agents with permission prompts turned off, "+
			"so you will be asked to type I accept the risk. A Multipass virtual machine is safer.", "")
	}

	// Packages.
	var missing []string
	for _, t := range f.Tools {
		if !t.Found {
			missing = append(missing, t.Name)
		}
	}
	missingList := strings.Join(missing, ", ")
	toolsMissing := len(missing) > 0
	switch {
	case !toolsMissing:
		add(StepPackages, Done, "git, gh, jq and ruby are installed.", "")
	case f.OS == "linux" && f.PackageManager == "apt-get", f.OS == "darwin" && f.PackageManager == "brew":
		add(StepPackages, ToDo, "Missing: "+missingList+". afk will install them with "+f.PackageManager+".", "")
	case f.OS == "darwin":
		add(StepPackages, Cannot, "Missing: "+missingList+", and Homebrew is not installed.",
			"Install Homebrew from brew.sh, then run afk install again.")
	default:
		add(StepPackages, Cannot, "Missing: "+missingList+", and afk does not know this system's package manager.",
			"Install "+missingList+" with your system's package manager, then run afk install again.")
	}

	// Agent CLI.
	if f.AgentCLI.Found {
		add(StepAgentCLI, Done, "The agent CLI is installed.", "")
	} else {
		add(StepAgentCLI, ToDo, "The agent CLI is not installed yet.", "")
	}

	// Agent login.
	switch {
	case !f.AgentCLI.Found:
		add(StepAgentLogin, ToDo, "You can sign in after the agent CLI is installed.", "")
	case f.AgentLogin == Yes:
		add(StepAgentLogin, Done, "The agent CLI is signed in.", "")
	case f.AgentLogin == Unknown:
		add(StepAgentLogin, ToDo, "afk could not tell whether the agent CLI is signed in; you will be asked to sign in.", "")
	default:
		add(StepAgentLogin, ToDo, "The agent CLI is not signed in; you will be asked to sign in.", "")
	}

	// Unattended mode.
	switch f.Unattended {
	case provider.UnattendedSet:
		add(StepUnattended, Done, "The agent CLI is set to run without permission prompts.", "")
	case provider.UnattendedNotSet:
		add(StepUnattended, ToDo, "afk will turn off the agent CLI's permission prompts in its settings file.", "")
	case provider.UnattendedUnsupported:
		add(StepUnattended, Cannot, "This agent CLI has no unattended mode afk knows.",
			"Use an agent CLI that afk supports for unattended runs.")
	default:
		fix := f.UnattendedProblem
		if fix == "" {
			fix = "The agent CLI's settings file cannot be read. Fix or remove it, then run afk install again."
		}
		add(StepUnattended, Cannot, "The agent CLI's settings file is broken, so afk will not overwrite it.", fix)
	}

	// Plugin.
	switch {
	case f.AgentPlugin == Yes:
		add(StepPlugin, Done, "The afk plugin is installed.", "")
	case !f.AgentCLI.Found:
		add(StepPlugin, ToDo, "The plugin can be installed after the agent CLI is installed.", "")
	default:
		add(StepPlugin, ToDo, "The afk plugin is not installed yet.", "")
	}

	// GitHub login.
	ghFound := false
	for _, t := range f.Tools {
		if t.Name == "gh" && t.Found {
			ghFound = true
		}
	}
	switch {
	case f.GitHubLogin == Yes && f.GitUsesGitHub == Yes:
		add(StepGitHubLogin, Done, "You are signed in to GitHub and git uses that login.", "")
	case !ghFound:
		add(StepGitHubLogin, ToDo, "You can sign in to GitHub after the packages are installed.", "")
	case f.GitHubLogin != Yes:
		add(StepGitHubLogin, ToDo, "You are not signed in to GitHub (or afk could not tell); you will be asked to sign in.", "")
	default:
		add(StepGitHubLogin, ToDo, "git is not set up to use your GitHub login; afk will set that up.", "")
	}

	// Git identity.
	var noIdent []string
	if f.GitName == "" {
		noIdent = append(noIdent, "name")
	}
	if f.GitEmail == "" {
		noIdent = append(noIdent, "email")
	}
	if len(noIdent) == 0 {
		add(StepGitIdentity, Done, "git knows your name and email.", "")
	} else {
		add(StepGitIdentity, ToDo, "git is missing your "+strings.Join(noIdent, " and ")+"; you will be asked for it.", "")
	}

	// Project location.
	switch {
	case f.ProjectLocationProblem != "":
		add(StepProjectLocation, Cannot, "The project location has a problem.", f.ProjectLocationProblem)
	case f.ProjectLocationSet:
		add(StepProjectLocation, Done, "Projects live in "+f.ProjectLocation+".", "")
	default:
		add(StepProjectLocation, ToDo, "No project location is chosen yet; the default is ~/code.", "")
	}

	// Web page.
	switch f.WebPassword {
	case Yes:
		add(StepWebPage, Done, "The web page has a password.", "")
	case Unknown:
		add(StepWebPage, ToDo, "The password file cannot be read; you will set a new one.", "")
	default:
		add(StepWebPage, ToDo, "The web page has no password yet; you will set one.", "")
	}

	// Service.
	s := f.Service
	switch {
	case !f.BinHasAfk:
		add(StepService, Cannot, "The service runs ~/bin/afk and it is not there.",
			"Put afk in ~/bin first (the install script does this), then run afk install again.")
	default:
		var lack []string
		if s.Installed != Yes {
			lack = append(lack, "installed")
		}
		if s.Enabled != Yes {
			lack = append(lack, "enabled")
		}
		if s.Running != Yes {
			lack = append(lack, "running")
		}
		if f.OS == "linux" && s.Linger != Yes {
			lack = append(lack, "kept running after you log out")
		}
		if len(lack) == 0 {
			add(StepService, Done, "The afk service is installed, enabled and running.", "")
		} else {
			add(StepService, ToDo, fmt.Sprintf("The afk service still needs to be: %s.", strings.Join(lack, ", ")), "")
		}
	}

	// Check.
	allDone := true
	for _, st := range p.Steps {
		if st.State != Done {
			allDone = false
		}
	}
	switch {
	case !f.BinOnPath:
		add(StepCheck, ToDo, "~/bin is not on your PATH. Add the line export PATH=\"$HOME/bin:$PATH\" to your shell's startup file "+
			"(~/.bashrc or ~/.zshrc), then open a new terminal.", "")
	case allDone:
		add(StepCheck, Done, "Every step is done.", "")
	default:
		add(StepCheck, ToDo, "afk will check everything once the steps above are done.", "")
	}
	return p
}
