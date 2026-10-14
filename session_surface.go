package main

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/host"
	"github.com/dphaener/afk/internal/session"
)

// The three session actions the web page offers besides clean: start an
// autonomous session, restart a stopped or wedged one, and set a label. Each
// is a root entry point that returns its outcome and prints nothing, in the
// shape of cleanSessionForSurface, because the daemon that serves the page
// must never write on its own streams and the page shows the text as it is.
//
// internal/web names none of session, host or discover, so everything that
// touches the world enters through the deps structs below and tests use fakes.

// Outcomes of a session action. They are the strings internal/web's
// SessionActionResult carries, so the wiring is a copy and not a mapping.
const (
	sessionActionDone    = "done"
	sessionActionRefused = "refused"
	sessionActionFailed  = "failed"
)

// Limits the page also checks. They are repeated here because the root is the
// last gate: a caller that skips the page's check still meets them.
const (
	sessionDescriptionMax = 5000
	sessionLabelMax       = 128
)

// sessionActionResult is one action's answer. Text is a plain sentence with no
// "afk ...:" prefix, shown on the page as it is. Refused means nothing was
// attempted; failed means it was attempted and went wrong, so something may
// have changed.
type sessionActionResult struct {
	Outcome string
	Text    string
}

func sessionRefused(format string, a ...any) sessionActionResult {
	return sessionActionResult{Outcome: sessionActionRefused, Text: fmt.Sprintf(format, a...)}
}

func sessionFailed(format string, a ...any) sessionActionResult {
	return sessionActionResult{Outcome: sessionActionFailed, Text: fmt.Sprintf(format, a...)}
}

// newSurfaceDeps is what starting a session touches. Session is the
// internal/session seam (git, git-spice, the host executable); its Stdout and
// Stderr are always replaced by buffers. Build, Run and CheckProvider default
// to the real ones and exist so a test can see the Config that was built and
// fail the run without a repository.
type newSurfaceDeps struct {
	Session       session.Deps
	Build         func(cfg session.Config, d session.Deps) (*session.Plan, error)
	Run           func(p *session.Plan) error
	CheckProvider func(name string, scratch, autonomous bool) string
}

func defaultNewSurfaceDeps() newSurfaceDeps {
	return newSurfaceDeps{Session: session.DefaultDeps()}
}

func (d newSurfaceDeps) withDefaults() newSurfaceDeps {
	if d.Build == nil {
		d.Build = session.Build
	}
	if d.Run == nil {
		d.Run = func(p *session.Plan) error { return p.Run() }
	}
	if d.CheckProvider == nil {
		d.CheckProvider = resolveProviderArg
	}
	return d
}

// newAutonomousForSurface starts what `afk new --autonomous --description D
// <name>` starts, in the project at root, and says how it went.
//
// The Config is exactly that command's: Autonomous true, and no Unattended, no
// base, no provider flag. Unattended is the daemon's permission mode; a session
// the owner starts from a page runs like one they start at a terminal, so a
// permission prompt waits for them in `afk dash` rather than being waved
// through by someone who may be on a phone. No base means Build picks the
// trunk (or the git-spice default) as the command does when it has no
// terminal to ask on. The description is required because an autonomous
// session has nobody to ask what it is for.
//
// A failed plugin preflight is not a refusal: the command prints it as a
// warning and goes on, so here it is a sentence appended to the done text.
func newAutonomousForSurface(root, name, description string, d newSurfaceDeps) sessionActionResult {
	d = d.withDefaults()
	if strings.TrimSpace(description) == "" {
		return sessionRefused("Say what the session should do. An autonomous session has nobody to ask, so it needs a description.")
	}
	if utf8.RuneCountInString(description) > sessionDescriptionMax {
		return sessionRefused("The description is too long. Keep it to %d characters or fewer.", sessionDescriptionMax)
	}
	if msg := d.CheckProvider("", false, true); msg != "" {
		return sessionRefused("%s", strings.TrimPrefix(msg, "afk new: "))
	}
	cfg := session.Config{
		Name:        name,
		Description: description,
		RepoDir:     root,
		Autonomous:  true,
	}
	var out, errOut bytes.Buffer
	sd := d.Session
	sd.Stdout, sd.Stderr = &out, &errOut
	plan, err := d.Build(cfg, sd)
	if err != nil {
		return sessionRefused("%s", err)
	}
	if err := d.Run(plan); err != nil {
		return sessionFailed("Starting %s failed: %s", name, err)
	}
	text := fmt.Sprintf("Started %s. It runs on its own from plan to reviewed commits; open afk dash or afk attach to see it.", name)
	if !plan.PreflightOK && plan.PreflightMsg != "" {
		text += " Warning: " + plan.PreflightMsg
	}
	return sessionActionResult{Outcome: sessionActionDone, Text: text}
}

// restartSurfaceDeps is what a restart touches. Discover reads the rows;
// SpawnHost starts a host for a stopped session; RequestRestart asks a running
// wedged host to restart its child.
type restartSurfaceDeps struct {
	Discover       func(root string) ([]discover.SessionState, error)
	SpawnHost      func(spec session.HostSpawn) (session.HostProcess, error)
	RequestRestart func(worktree, name string) (refused string, err error)
}

// restartReplyWait is how long a restart waits for the host to say how it
// went: the daemon's wait, which covers the host's nine-second signal ladder.
// It is under the web server's 30 s write timeout.
const restartReplyWait = 15 * time.Second

func defaultRestartSurfaceDeps() restartSurfaceDeps {
	return restartSurfaceDeps{
		Discover: func(root string) ([]discover.SessionState, error) {
			return discover.Discover(root, discover.DefaultDeps())
		},
		SpawnHost: func(spec session.HostSpawn) (session.HostProcess, error) {
			return session.SpawnHost(spec, session.Deps{})
		},
		RequestRestart: func(worktree, name string) (string, error) {
			return host.RequestRestart(worktree, name, daemonSendWait, restartReplyWait)
		},
	}
}

// surfaceRow is the row a name means: the first with that name, which
// discovery orders status-backed first. The page and `afk attach` read a name
// the same way, so one name is never two sessions depending on where it was
// typed.
func surfaceRow(rows []discover.SessionState, name string) (discover.SessionState, bool) {
	for _, r := range rows {
		if r.Name == name {
			return r, true
		}
	}
	return discover.SessionState{}, false
}

// surfaceWorktreeFault reports a row whose worktree is missing or is not afk's:
// nothing may be written there, and there is no host to restart.
func surfaceWorktreeFault(r discover.SessionState) bool {
	return r.Kind == session.KindWorktreeNoSession || r.Kind == session.KindWorktreeMissing
}

// restartSessionForSurface restarts a stopped or wedged planned session.
//
// The worktree comes from discovery, never from the caller. A running host
// that is not wedged is refused: a restart replaces the agent's process and
// drops whoever is attached, which a healthy session must never suffer from a
// stale page. A one-off has no status.json to resume from, and the planning
// session is owned by `afk plan`. Exit is never sent: it would end the session
// for good, which is what clean is for.
func restartSessionForSurface(root, name string, d restartSurfaceDeps) sessionActionResult {
	if d.Discover == nil || d.SpawnHost == nil || d.RequestRestart == nil {
		def := defaultRestartSurfaceDeps()
		if d.Discover == nil {
			d.Discover = def.Discover
		}
		if d.SpawnHost == nil {
			d.SpawnHost = def.SpawnHost
		}
		if d.RequestRestart == nil {
			d.RequestRestart = def.RequestRestart
		}
	}
	rows, err := d.Discover(root)
	if err != nil {
		return sessionFailed("Could not list the sessions: %s", err)
	}
	row, ok := surfaceRow(rows, name)
	if !ok {
		return sessionRefused("There is no session named %s.", name)
	}
	switch {
	case row.Name == session.PlanSessionName:
		return sessionRefused("%s is the planning session. Open it with afk plan.", name)
	case row.Scratch:
		return sessionRefused("%s is a one-off session, which has no plan to resume from. Restart it from afk dash.", name)
	case surfaceWorktreeFault(row):
		return sessionRefused("%s has a problem with its worktree, so there is nothing to restart. Clean it, or fix the worktree.", name)
	}
	logPath := filepath.Join(row.WorktreePath, ".afk", name, "host.log")
	if !row.HostRunning {
		if _, err := d.SpawnHost(session.HostSpawn{Name: name, Worktree: row.WorktreePath}); err != nil {
			return sessionFailed("Starting the host for %s failed: %s. See %s.", name, err, logPath)
		}
		return sessionActionResult{Outcome: sessionActionDone, Text: fmt.Sprintf("Started the host for %s again. The agent resumes from its files.", name)}
	}
	if row.WedgedSince == "" {
		return sessionRefused("%s is running and answering, so it was not restarted. Restart only replaces a stopped or stuck agent.", name)
	}
	refused, err := d.RequestRestart(row.WorktreePath, name)
	switch {
	case errors.Is(err, host.ErrRestartUnknown):
		return sessionFailed("The restart of %s was requested, but the host did not say how it went. Check it in afk dash.", name)
	case err != nil:
		return sessionFailed("Restarting %s failed: %s. See %s.", name, err, logPath)
	case refused != "":
		return sessionFailed("The host refused to restart %s: %s. See %s.", name, refused, logPath)
	}
	return sessionActionResult{Outcome: sessionActionDone, Text: fmt.Sprintf("Restarted %s. The agent is resuming its conversation.", name)}
}

// labelSurfaceDeps is what setting a label touches.
type labelSurfaceDeps struct {
	Discover   func(root string) ([]discover.SessionState, error)
	WriteLabel func(worktree, name, label string) error
}

func defaultLabelSurfaceDeps() labelSurfaceDeps {
	return labelSurfaceDeps{
		Discover:   defaultRestartSurfaceDeps().Discover,
		WriteLabel: session.WriteLabel,
	}
}

// setLabelForSurface sets, or with a blank label clears, a session's display
// label. The worktree is the discovered row's, never the caller's: a label is
// a file written into a worktree, so a name must not be able to point it
// elsewhere. A worktree-fault row is refused because there is nowhere it would
// be right to write (the dashboard refuses the same rows).
func setLabelForSurface(root, name, label string, d labelSurfaceDeps) sessionActionResult {
	if d.Discover == nil {
		d.Discover = defaultLabelSurfaceDeps().Discover
	}
	if d.WriteLabel == nil {
		d.WriteLabel = session.WriteLabel
	}
	label = strings.TrimSpace(label)
	if utf8.RuneCountInString(label) > sessionLabelMax {
		return sessionRefused("The label is too long. Keep it to %d characters or fewer.", sessionLabelMax)
	}
	rows, err := d.Discover(root)
	if err != nil {
		return sessionFailed("Could not list the sessions: %s", err)
	}
	row, ok := surfaceRow(rows, name)
	if !ok {
		return sessionRefused("There is no session named %s.", name)
	}
	if surfaceWorktreeFault(row) {
		return sessionRefused("%s has a problem with its worktree, so a label cannot be saved for it. Clean it, or fix the worktree.", name)
	}
	if err := d.WriteLabel(row.WorktreePath, name, label); err != nil {
		return sessionFailed("Could not save the label for %s: %s", name, err)
	}
	if label == "" {
		return sessionActionResult{Outcome: sessionActionDone, Text: fmt.Sprintf("Cleared the label of %s.", name)}
	}
	return sessionActionResult{Outcome: sessionActionDone, Text: fmt.Sprintf("Labelled %s %q.", name, label)}
}
