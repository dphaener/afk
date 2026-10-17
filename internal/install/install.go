// Package install turns what afk found out about a machine into the ordered
// steps of `afk install`, each done, to do or cannot, and says how risky the
// machine is to run afk on.
//
// The decision (Build) is pure: it takes a Facts value and does no I/O, reads
// no clock and no environment. That is the repo's rule for anything that
// branches on the environment, and it makes every state of every step a table
// row. Finding the facts out is a separate, impure function with every
// command and file read injected, so it can be faked in tests and so this
// file stays free of anything that could hang.
//
// No agent vendor is named here. Anything that depends on which agent CLI is
// in use arrives as a plain fact (the CLI was found, the setting is broken)
// and the text says "the agent CLI"; the names live in internal/provider.
package install

import "github.com/dphaener/afk/internal/provider"

// Check is a yes/no answer the probe may be unable to give. Unknown means the
// probe errored or timed out; it must never be folded into No, because "could
// not tell" and "no" lead to different advice.
type Check string

const (
	Yes     Check = "yes"
	No      Check = "no"
	Unknown Check = "unknown"
)

// Virt is what the machine reports about being virtual.
type Virt string

const (
	VirtNone      Virt = "none"
	VirtVM        Virt = "vm"
	VirtContainer Virt = "container"
	// VirtUnknown means the detection command failed or timed out.
	VirtUnknown Virt = "unknown"
)

// Risk is how dangerous it is to run afk (agents with permissions bypassed)
// on this machine.
type Risk string

const (
	RiskDedicated Risk = "dedicated"
	RiskVM        Risk = "vm"
	RiskPersonal  Risk = "personal"
	// RiskUnknown is treated like personal by the installer.
	RiskUnknown Risk = "unknown"
)

// State is where one install step stands.
type State string

const (
	Done   State = "done"
	ToDo   State = "todo"
	Cannot State = "cannot"
)

// StepID names a step. The consts are in install order.
type StepID string

const (
	StepSafety          StepID = "safety"
	StepPackages        StepID = "packages"
	StepAgentCLI        StepID = "agent-cli"
	StepAgentLogin      StepID = "agent-login"
	StepUnattended      StepID = "unattended"
	StepPlugin          StepID = "plugin"
	StepGitHubLogin     StepID = "github-login"
	StepGitIdentity     StepID = "git-identity"
	StepProjectLocation StepID = "project-location"
	StepWebPage         StepID = "web-page"
	StepService         StepID = "service"
	StepCheck           StepID = "check"
)

// Tool is one program afk needs and whether the probe found it.
type Tool struct {
	Name  string
	Found bool
	Path  string
}

// ServiceFacts describes the background service. Linger is only read on
// Linux (it keeps a user service running after logout).
type ServiceFacts struct {
	Installed Check
	Enabled   Check
	Running   Check
	Linger    Check
}

// Facts is everything Build needs to know about the machine.
type Facts struct {
	OS            string
	Distro        string
	DistroVersion string
	Arch          string
	Root          bool
	Virt          Virt
	Desktop       bool
	// PackageManager is "apt-get", "brew" or "".
	PackageManager string
	// Tools lists git, gh, jq and ruby, in that order.
	Tools    []Tool
	AgentCLI Tool

	AgentLogin Check
	// Unattended and UnattendedProblem come from the provider package, which
	// can tell "not set yet" from "file broken".
	Unattended        provider.UnattendedSetting
	UnattendedProblem string
	AgentPlugin       Check

	// BinOnPath: ~/bin is on PATH. BinHasAfk: ~/bin/afk exists. BinIsThis: it
	// is the running binary.
	BinOnPath bool
	BinHasAfk bool
	BinIsThis bool

	GitHubLogin   Check
	GitUsesGitHub Check
	GitName       string
	GitEmail      string

	ProjectLocation        string
	ProjectLocationSet     bool
	ProjectLocationProblem string

	// WebPassword: Yes is a valid record, No no file, Unknown unreadable or
	// damaged.
	WebPassword Check
	WebBind     string
	Tailscale   bool

	Service ServiceFacts

	// RiskAccepted is set by the caller after the person has passed the safety
	// screen in this run, never by Probe. It is held in memory for one run on
	// purpose: nothing on disk may say "this machine is safe", so every run
	// of a personal machine asks again.
	RiskAccepted bool
}

// Step is one line of the plan. Fix is only filled for Cannot.
type Step struct {
	ID     StepID
	Title  string
	State  State
	Reason string
	Fix    string
}

// Plan is the risk and the twelve steps, in order.
type Plan struct {
	Risk  Risk
	Steps []Step
}
