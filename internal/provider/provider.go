// Package provider is the seam between afk and the agent CLI a session
// host runs. Before it existed the choice of `claude` was hardcoded at three
// places: childArgv in internal/session/create.go (launch argv),
// resumeArgv in internal/host/restart.go (wedged-restart resume argv), and
// the plugin preflight in internal/session/create.go that shells out to
// `claude plugin list`. A provider owns all three so adding an agent CLI is
// one new type here rather than a hunt through the session and host code.
//
// Every method is pure apart from three. Preflight and Usage reach the
// outside world only through an injected runner, and SessionTokens
// (tokens.go) only reads files below the home directory it is handed, so the
// whole matrix is table-testable without any CLI installed.
package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const (
	// ClaudeName is the Claude Code provider's name.
	ClaudeName = "claude"
	// CodexName is the OpenAI Codex CLI provider's name.
	CodexName = "codex"
	// OpenCodeName is the OpenCode CLI provider's name.
	OpenCodeName = "opencode"
	// Default is the provider used when none is recorded for a session, so
	// sessions that predate providers behave exactly as they always did.
	Default = ClaudeName
)

// RunFunc runs a command in dir and returns its stdout. It matches the
// signature of session's defaultRun so callers can pass it straight through.
type RunFunc = func(dir, name string, args ...string) (string, error)

// Provider describes how to launch, resume, and preflight one agent CLI.
type Provider interface {
	// Name is the stable identifier persisted in the provider sidecar and
	// host.json, and accepted by `afk new --provider`.
	Name() string
	// LaunchArgv is the child argv for a new session host. scratch selects
	// a bare one-off session with no conductor; autonomous selects the
	// conductor's no-questions mode.
	LaunchArgv(name string, scratch, autonomous bool) []string
	// PlanArgv is the child argv of an attended planning session: the agent
	// running the master-plan skill with its owner at the terminal. It takes
	// no session name because a project has one planning session and the skill
	// finds its own state in the project folder. A provider whose plugin has
	// no such skill returns a nil argv and ErrNoPlanSession, so a caller can
	// refuse before anything is started and never falls back to a bare agent.
	PlanArgv() ([]string, error)
	// ResumeArgv is the child argv that resumes an existing agent session
	// by its id, used when the host restarts a wedged child.
	ResumeArgv(sessionID string) []string
	// Preflight is a best-effort, non-fatal check that the afk plugin is
	// reachable for this provider. It reports manual install steps when it
	// is not. All process execution goes through run.
	Preflight(dir string, run func(dir, name string, args ...string) (string, error)) (ok bool, manualSteps string)
	// Usage reports how much of each usage window is spent. The command
	// runner is a parameter, not an import of os/exec, for the same reason
	// as Preflight: tests inject a fake and never touch a real CLI. A
	// provider with no way to report returns ErrUsageUnsupported.
	Usage(ctx context.Context, d UsageDeps) (UsageSnapshot, error)
	// SessionTokens returns one record per model call of a session and its
	// sub-agents, for the token ledger. A provider that cannot read its
	// sessions' token use returns ErrTokensUnsupported; one that can but
	// finds nothing for the session returns ErrNoTranscript. It reads files
	// under q.Home only and runs nothing.
	SessionTokens(q TokenQuery) ([]TokenRecord, error)
	// Unattended returns a copy of a launch or resume argv with the
	// provider's unattended permission mode named on the command line, so a
	// daemon-run session never stops at a permission prompt nobody is there
	// to answer. A provider with no such mode afk knows returns
	// ErrUnattendedUnsupported. See unattended.go.
	Unattended(argv []string) ([]string, error)
	// UnattendedPreflight is a best-effort, non-fatal check that the
	// unattended mode will actually be accepted on this machine. It reads
	// only through d and reports plain-language manual steps when it will
	// not be. See unattended.go.
	UnattendedPreflight(d UnattendedDeps) (ok bool, manualSteps string)
	// Login reports whether the agent CLI is logged in, through the injected
	// runner. A provider with no way to tell returns ErrLoginUnsupported.
	// See login.go.
	Login(ctx context.Context, d LoginDeps) (LoginStatus, error)
}

// order is the stable listing order for Names.
var order = []Provider{claude{}, codex{}, opencode{}}

// Names returns the valid provider names in stable order, for usage text.
func Names() []string {
	names := make([]string, len(order))
	for i, p := range order {
		names[i] = p.Name()
	}
	return names
}

// Lookup resolves a provider by name. The empty string means Default, which
// is how sessions with no recorded provider keep resolving to Claude.
func Lookup(name string) (Provider, error) {
	if name == "" {
		name = Default
	}
	for _, p := range order {
		if p.Name() == name {
			return p, nil
		}
	}
	return nil, fmt.Errorf("unknown provider %q (valid: %s)", name, strings.Join(Names(), ", "))
}

// ErrNoPlanSession is returned by PlanArgv for a provider that cannot run a
// planning session. The master-plan skill ships only in plugins/afk, the
// default provider's plugin; the OpenCode port does not carry it (see the lag
// note in plugins/afk-opencode/PROVENANCE.md). The text is shown to the owner
// as it is, so it says what to do and names no provider: the caller adds which
// one was asked for.
var ErrNoPlanSession = errors.New("planning sessions are not available with this agent yet; run `afk plan` with the default agent instead")

// CodexInteractiveConflictMsg is the exact usage error for the codex provider
// with neither autonomous nor scratch. Codex's structured question tool exists
// only in Plan mode, so the interactive planning conductor cannot run there;
// only the autonomous conductor and one-off sessions can. It carries the
// "afk new: " prefix because `afk new` prints it verbatim.
const CodexInteractiveConflictMsg = "afk new: --provider codex requires --autonomous or --scratch: Codex sessions run autonomous-only"

// CheckMode reports whether the named provider can run a session in the given
// mode. It lives here, not in package main, so the dashboard's new-session form
// can validate inline with the same rule and the same text `afk new` prints,
// instead of duplicating the constraint and drifting. An unknown name returns
// Lookup's error; "" and "claude" accept every mode.
func CheckMode(name string, scratch, autonomous bool) error {
	if _, err := Lookup(name); err != nil {
		return err
	}
	if name == CodexName && !scratch && !autonomous {
		return errors.New(CodexInteractiveConflictMsg)
	}
	return nil
}

// claude runs Claude Code. Its argv must stay byte-identical to what afk
// built before providers existed.
type claude struct{}

func (claude) Name() string { return ClaudeName }

func (claude) LaunchArgv(name string, scratch, autonomous bool) []string {
	if scratch {
		return []string{"claude"}
	}
	cmd := "/afk:go --session " + name
	if autonomous {
		cmd += " --autonomous"
	}
	return []string{"claude", cmd}
}

// planPrompt is the slash command that starts the master-plan skill. It is the
// only place the text appears in Go: everything else asks PlanArgv.
const planPrompt = "/afk:master-plan"

// PlanArgv carries no session name and no --autonomous: a planning session is
// attended by definition, and the skill reads .afk/plan/ in the project.
func (claude) PlanArgv() ([]string, error) {
	return []string{"claude", planPrompt}, nil
}

func (claude) ResumeArgv(sessionID string) []string {
	return []string{"claude", "--resume", sessionID}
}

func (claude) Preflight(dir string, run func(dir, name string, args ...string) (string, error)) (bool, string) {
	manual := "afk plugin / `/afk:go` not detected in this repo.\n" +
		"      Install it, then re-run, or launch manually:\n" +
		"        1. afk plugin install --target claude\n" +
		"        2. Confirm /afk:go is listed via /help"
	out, err := run(dir, "claude", "plugin", "list")
	if err != nil {
		return false, manual
	}
	if !strings.Contains(strings.ToLower(out), "afk") {
		return false, manual
	}
	return true, ""
}

// codexConductorPrompt is the prompt prefix that invokes the afk conductor
// skill in a Codex session. The `$skill` mention syntax is unverified against
// Codex 0.146; it lives in this one const so correcting it is a one-line
// change.
const codexConductorPrompt = "$afk:conductor"

// codex runs the OpenAI Codex CLI. Codex's structured question tool exists
// only in Plan mode, so afk runs it for scratch and autonomous conductor
// sessions only, and adds no approval, sandbox, or hook-trust bypass flags.
type codex struct{}

func (codex) Name() string { return CodexName }

func (codex) LaunchArgv(name string, scratch, _ bool) []string {
	if scratch {
		return []string{"codex"}
	}
	// --autonomous is unconditional: a non-autonomous conductor would need
	// to ask questions, which Codex cannot do outside Plan mode.
	return []string{"codex", codexConductorPrompt + " --session " + name + " --autonomous"}
}

// PlanArgv refuses for two reasons that each suffice: the planning session
// asks its owner questions, which Codex can do only in Plan mode (see
// CodexInteractiveConflictMsg), and no `$skill` mention for the master-plan
// skill has been tried against the CLI.
func (codex) PlanArgv() ([]string, error) { return nil, ErrNoPlanSession }

func (codex) ResumeArgv(sessionID string) []string {
	return []string{"codex", "resume", sessionID}
}

func (codex) Preflight(dir string, run func(dir, name string, args ...string) (string, error)) (bool, string) {
	manual := "afk plugin not detected in Codex.\n" +
		"      Install it, then re-run:\n" +
		"        afk plugin install --target codex\n" +
		"      Codex sessions run autonomous-only (--autonomous or --scratch)."
	out, err := run(dir, "codex", "plugin", "list")
	if err != nil {
		return false, manual
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.HasPrefix(fields[0], "afk@") {
			continue
		}
		status := strings.ToLower(strings.Join(fields[1:], " "))
		if strings.Contains(status, "not installed") {
			continue
		}
		if strings.Contains(status, "installed") {
			return true, ""
		}
	}
	return false, manual
}

// opencodeConductorPrompt is the prompt prefix that invokes the afk
// conductor in an OpenCode session. Passing it through the TUI's --prompt
// flag is unverified against the real TUI; the afk port's prompt hook
// expands a leading `/afk-go`, which covers it either way. It lives in this
// one const so a correction is a one-line change.
const opencodeConductorPrompt = "/afk-go"

// opencode runs the OpenCode CLI. afk adds no permission-bypass flag
// (never --auto): that stays the user's call.
type opencode struct{}

func (opencode) Name() string { return OpenCodeName }

func (opencode) LaunchArgv(name string, scratch, autonomous bool) []string {
	// --standalone keeps the session in the PTY child. Without it the
	// OpenCode background service owns the session, so killing or restarting
	// the child would not control the conversation.
	if scratch {
		return []string{"opencode", "--standalone"}
	}
	prompt := opencodeConductorPrompt + " --session " + name
	if autonomous {
		prompt += " --autonomous"
	}
	return []string{"opencode", "--standalone", "--prompt", prompt}
}

// PlanArgv refuses because the OpenCode port has no master-plan skill to run.
func (opencode) PlanArgv() ([]string, error) { return nil, ErrNoPlanSession }

func (opencode) ResumeArgv(sessionID string) []string {
	// --standalone for the same ownership reason as LaunchArgv.
	return []string{"opencode", "--standalone", "-s", sessionID}
}

func (opencode) Preflight(dir string, run func(dir, name string, args ...string) (string, error)) (bool, string) {
	manual := "afk plugin not detected in OpenCode.\n" +
		"      Install it into ~/.config/opencode/plugins/afk/ with\n" +
		"      `afk plugin install --target opencode`, then re-run.\n" +
		"      Confirm it is listed via `opencode plugin list`."
	out, err := run(dir, "opencode", "plugin", "list")
	if err != nil {
		return false, manual
	}
	// Rows are `ID VERSION SOURCE`; the header's first field is "ID", so an
	// exact match on "afk" skips it, blank lines, and ids like afk-other.
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "afk" {
			return true, ""
		}
	}
	return false, manual
}
