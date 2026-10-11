// Package notify builds and delivers afk's desktop notifications.
//
// The CORE is pure and must stay that way: notify.go (Format, Select), argv.go
// (AlerterArgs, OsascriptArgs) and target.go (ResolveTarget) perform no exec, read
// no env directly, and touch no files — every environment value they need arrives
// as a parameter (Select takes a lookPath func, ResolveTarget a getenv func). That
// is what makes the whole toast-shape and click-decision matrix table-testable
// without a notifier, a tmux server, or a TTY.
//
// Two files are deliberately impure, and they are confined:
//
//   - watch.go execs alerter, tmux and osascript. Everything in it that BRANCHES is
//     still a pure function (IsFocusOutcome, Focusable, FocusPlan, PaneListed,
//     ClientListed, ValidTerminalApp); the exec lives in thin runners that take
//     those results as parameters.
//   - icon.go extracts the embedded app-icon PNG to a cache directory on first use,
//     because the binary is installed away from the repo so a repo-relative asset
//     path breaks after `make install`.
//
// Both run only in the detached watcher child, never on a caller's critical path.
// Keep new impurity inside these files: a Format or an argv builder that
// reached for the environment on its own would take the matrix above with it.
//
// The package also sends push notifications through ntfy. ntfy.go is the third
// confined impure file: it makes one HTTP request (Ntfy.Send) and its only
// branching, which inbox items notify and how loudly, is the pure NtfyPriority.
// Unlike the two files above it is called from the `afk inbox` command, never
// from the detached watcher. NtfyPriority takes plain kind and blocking values
// because notify must not import internal/inbox.
//
// internal/probe imports this for backend selection, never the reverse. Import
// boundaries: internal/notify imports only stdlib and internal/dashmeta. Never
// imports internal/probe, internal/session, internal/host, internal/state or
// internal/inbox. The chain is session → probe → notify → dashmeta, acyclic.
package notify

type Toast struct {
	Title    string
	Subtitle string
	Body     string
	Group    string
	Sound    string // Empty by default; per-backend sound names set by W2.T1 (alerter:"default", osascript:"Ping"). Empty is safe because a bogus osascript sound name exits 0 silently.
}

type Input struct {
	PlanName string
	Repo     string
	Message  string
}

// Format produces the notification toast shape confirmed against live macOS toasts
// in W0.T1. Title carries the repo (the project identity users scan for), subtitle
// carries the session/plan name. The ● body prefix was removed because it renders
// far heavier in Notification Center than in a terminal.
func Format(in Input) Toast {
	planName := in.PlanName
	if planName == "" {
		planName = "afk session"
	}

	message := in.Message
	if message == "" {
		message = "Claude needs your attention"
	}

	var title string
	if in.Repo != "" {
		title = "afk · " + in.Repo
	} else {
		title = "afk"
	}

	subtitle := planName
	body := message

	var group string
	if in.Repo != "" {
		group = "afk-" + in.Repo + "-" + planName
	} else {
		group = "afk-" + planName
	}

	return Toast{
		Title:    title,
		Subtitle: subtitle,
		Body:     body,
		Group:    group,
	}
}

type Backend string

const (
	BackendAlerter   Backend = "alerter"
	BackendOsascript Backend = "osascript"
)

// Backends returns notification backends in preference order. terminal-notifier
// is absent because its last release (2.0.0 from 2017) is Intel-only and its own
// README redirects to alerter for sticky notifications. notify-send is absent
// because this implementation is macOS-only by design.
func Backends() []Backend {
	return []Backend{BackendAlerter, BackendOsascript}
}

func Select(lookPath func(string) (string, error)) (Backend, string, bool) {
	for _, backend := range Backends() {
		path, err := lookPath(string(backend))
		if err == nil {
			return backend, path, true
		}
	}
	return "", "", false
}
