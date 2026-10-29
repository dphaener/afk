// watch.go is the one impure corner of internal/notify: it is the only file in
// the package that execs anything (alerter, tmux, osascript). The package doc in
// notify.go describes the pure formatting core, and that description still holds
// for every other file here — the split inside this file keeps the spirit of it:
// everything that BRANCHES is a pure function (IsFocusOutcome, Focusable,
// FocusPlan, PaneListed, ValidTerminalApp) and exec lives only in the thin
// runners at the bottom, which take the pure results as parameters. ClickRoute is
// the one decision that calls out, and only through the FocusRexFunc its caller
// hands in, so its tests pass a fake.
//
// The watcher lives beside the argv builders on purpose. Its decisions are made
// entirely out of this package's own vocabulary — alerter's outcome strings and
// Target — so splitting it into a new package would put the outcome strings and
// the argv that produces them in two places that have to agree.
package notify

import (
	"context"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/dphaener/afk/internal/dashmeta"
)

// WatchLifetime caps how long a watcher waits for the user to interact with a
// toast. This is REQUIRED, not defensive: alerter with no --timeout blocks until
// a human acts, and W0.T1 observed alerter processes still resident past their
// own --timeout in undelivered cases. Without this cap, an ignored toast leaves
// both alerter and the watcher resident forever, one pair per notification.
const WatchLifetime = 12 * time.Hour

// tmuxTimeout bounds every tmux invocation the watcher makes. A wedged tmux
// server must not hang the watcher: it holds no locks, but it does hold a
// process and a socket-connect attempt against a server that may be gone.
const tmuxTimeout = 3 * time.Second

// Focus outcome strings, as OBSERVED in W0.T1 (not read from documentation):
// alerter invoked with a SINGLE --actions value prints the literal action label
// ("Focus"), not "@ACTIONCLICKED". @CONTENTCLICKED and @ACTIONCLICKED are
// accepted too because they are what alerter prints for a body click and for a
// multi-action build respectively, and both mean the same thing here. Everything
// else — @CLOSED, @TIMEOUT, empty (the 12h cap fired, or alerter never ran) and
// any unknown token — is do-nothing.
const (
	OutcomeFocus          = "Focus"
	OutcomeContentClicked = "@CONTENTCLICKED"
	OutcomeActionClicked  = "@ACTIONCLICKED"
)

// terminalAppRe is the whitelist for AFK_TERMINAL_APP. The app name is the
// only user-supplied string this package ever interpolates into text rather than
// passing as its own argv element (it lands inside an AppleScript string
// literal), so it is validated against a character class that cannot terminate
// that literal or chain a statement: no quotes, no backslashes, no semicolons.
// A name that fails validation skips activation rather than being escaped —
// activation is cosmetic, and refusing is cheaper to reason about than escaping.
var terminalAppRe = regexp.MustCompile(`^[A-Za-z0-9 ._-]+$`)

// Action is one external command to run: the command name plus its argv,
// deliberately WITHOUT a shell. FocusPlan returns these so the decision about
// what to run is separable from the running.
type Action struct {
	Name string
	Args []string
}

// FocusRequest is what a honored click asks the dashboard to do: open and focus
// Session, published under GitCommonDir for the dashboard to pick up.
//
// It is separate from Target rather than more fields on it because the two answer
// different questions and are consumed by different mechanisms. Target says where
// the user's tmux client has to go; this says which session the dashboard should
// bring to the front once they arrive. Either can be present without the other,
// and the zero value is the ordinary degraded case — the click still switches the
// pane, it just lands on the dashboard's list instead of inside the session.
type FocusRequest struct {
	Session      string
	GitCommonDir string
	// TargetPID is the dashboard the request is addressed to. Requests are
	// per-dashboard rather than one shared file so that exactly ONE dashboard
	// honors a click: a shared file had every running dashboard open the same
	// session, and since internal/host serves exactly one client, the second
	// connection stole it out of the first's pane.
	//
	// The pid comes from the Entry ReadEntry chose — the first live one — which is
	// also the dashboard whose PaneID and ClientTTY are in Target, so the request
	// and the tmux switch always name the same dashboard. Zero means no dashboard
	// was found, and then no request is published.
	TargetPID int
}

// IsFocusOutcome reports whether alerter's stdout means "the user asked to be
// taken to the session". Whitespace is trimmed because alerter terminates the
// outcome with a newline.
func IsFocusOutcome(outcome string) bool {
	switch strings.TrimSpace(outcome) {
	case OutcomeFocus, OutcomeContentClicked, OutcomeActionClicked:
		return true
	}
	return false
}

// Focusable reports whether a toast should carry the Focus action button at all.
// It matches the click path's own preconditions exactly, so a rendered button
// always has something to do: a Rex window can be asked to come forward when
// both its ids are known (see ClickRoute), and otherwise FocusPlan needs a pane to
// switch to and a client tty to switch deterministically.
func Focusable(t Target) bool {
	if t.RexSession != "" && t.RexWindow != "" {
		return true
	}
	return t.PaneID != "" && t.ClientTTY != ""
}

// FocusPlan is the whole click decision, pure so the matrix is table-testable
// without a tmux server or a notification. paneExists and clientExists are
// supplied by the caller (see RunFocus) rather than checked here.
//
// Why the pane-existence check: a persistent alert can be clicked long after the
// dashboard that posted it died. A pane id is reused by tmux, so a stale-but-
// well-formed id would still be a valid target — switching to it would silently
// teleport the user's client to somewhere unrelated, which is worse than doing
// nothing.
//
// Why the CLIENT is re-checked too, and it is the same hazard: ClientTTY is
// recorded once at dashboard startup, so by click time that client may have
// detached — and a tty PATH is reused. Close the terminal that owned
// /dev/ttys000, open another, attach it, and the recorded string now names a
// different client. Switching that one would yank a window the user was doing
// something else in. Verifying the tty is still an attached client makes the
// target as fresh as the pane.
//
// Why switch-client keeps -c: W0.T1 verified both forms work from a detached
// process, but implicit client resolution picks whatever single client happens
// to be attached — wrong when several are, and "no current client" when none is.
// -c makes the target deterministic.
//
// An empty ClientTTY yields no actions for the same reason: the only way to
// switch without it is to drop -c, and activating the terminal without switching
// would raise a window showing an unrelated pane — the same confusion the
// existence check exists to prevent.
func FocusPlan(outcome string, t Target, paneExists, clientExists bool) []Action {
	if !IsFocusOutcome(outcome) {
		return nil
	}
	if t.PaneID == "" || t.ClientTTY == "" {
		return nil
	}
	if !paneExists || !clientExists {
		return nil
	}

	actions := []Action{{
		Name: "tmux",
		Args: []string{"switch-client", "-c", t.ClientTTY, "-t", t.PaneID},
	}}
	// Activation comes second: switching first means the terminal is already
	// showing the right pane by the time it is raised, so the user never sees
	// the wrong session flash.
	if ValidTerminalApp(t.TerminalApp) {
		actions = append(actions, Action{
			Name: "osascript",
			Args: []string{"-e", ActivateScript(t.TerminalApp)},
		})
	}
	return actions
}

// ValidTerminalApp reports whether app is safe to interpolate into the
// activation AppleScript. Empty is invalid: an empty TerminalApp means "switch
// the pane but skip activation" (see ResolveTarget), not "activate nothing".
func ValidTerminalApp(app string) bool {
	if app == "" {
		return false
	}
	return terminalAppRe.MatchString(app)
}

// ActivateScript is the one-line AppleScript that raises the terminal. Only
// reached for names ValidTerminalApp accepted.
func ActivateScript(app string) string {
	return `tell application "` + app + `" to activate`
}

// PaneListed reports whether paneID appears in `tmux list-panes -a -F
// '#{pane_id}'` output. The match is EXACT per line: a prefix match would let
// %1 match %12, and pane ids are handed out sequentially, so %1 and %12 coexist
// routinely. An empty paneID is never listed.
func PaneListed(out, paneID string) bool {
	return listedExactly(out, paneID)
}

// ClientListed reports whether clientTTY appears in `tmux list-clients -F
// '#{client_tty}'` output — i.e. whether that tty is still an ATTACHED client and
// therefore still a valid `switch-client -c` target.
//
// Same exact-line match as PaneListed, and for a sharper version of the same
// reason: tty paths are reused across terminal sessions, so /dev/ttys000 in a
// dashboard entry recorded an hour ago may be a different client now. Verified
// live that `tmux list-clients -F '#{client_tty}'` prints one bare tty path per
// line (`/dev/ttys000`), which is what makes the per-line comparison exact rather
// than a substring test.
func ClientListed(out, clientTTY string) bool {
	return listedExactly(out, clientTTY)
}

// listedExactly reports whether want is one whole line of out. An empty want is
// never listed: it would otherwise match the blank line that trails tmux's
// output.
func listedExactly(out, want string) bool {
	if want == "" {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}

// Watch runs alerter to completion, reads its outcome from stdout, and performs
// the focus plan for that outcome. It never returns an error and it never
// reports failure: the caller's only correct behavior is to exit 0, because a
// watcher that exited non-zero would surface as a failed Claude Code hook for
// something as inconsequential as a dead pane.
//
// This runs in the already-detached child (see the spawn in notify_cmd.go), so
// blocking here for up to WatchLifetime is the point.
func Watch(alerterPath string, args []string, t Target, fr FocusRequest, focusRex FocusRexFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), WatchLifetime)
	defer cancel()

	outcome := runAlerter(ctx, alerterPath, args)
	RunFocus(outcome, t, fr, focusRex)
}

// FocusRexFunc asks Rex to bring one window forward. The caller supplies it
// because this package must not import internal/rex: the watcher's decisions are
// made out of this package's own vocabulary, and the Rex call (with its deadline)
// belongs to the command that owns the Rex caller. A nil func means "Rex cannot
// be asked", which takes the tmux path.
type FocusRexFunc func(sessionID, windowID string) error

// Route is where a click went.
type Route int

const (
	// RouteNone: nothing to focus, or the outcome was not a click.
	RouteNone Route = iota
	// RouteRex: Rex was asked and said yes; tmux is not touched.
	RouteRex
	// RouteTmux: the existing tmux path (FocusPlan) runs.
	RouteTmux
)

// ClickRoute decides where a click goes, calling focusRex itself so the Rex
// attempt and its fallback are one decision. Rex goes first whenever the parent
// passed both Rex ids, because the parent passes them only when ChooseNotice
// picked Rex (a Rex-backed session's agent is not in any tmux pane). A Rex error
// (Rex down, the window closed, the 2-second deadline) falls back to tmux rather
// than to nothing: the pane, when one was recorded, is still a better place to
// land than nowhere, and FocusPlan re-checks it before switching.
func ClickRoute(outcome string, t Target, focusRex FocusRexFunc) Route {
	if !IsFocusOutcome(outcome) {
		return RouteNone
	}
	if t.RexSession != "" && t.RexWindow != "" && focusRex != nil {
		if focusRex(t.RexSession, t.RexWindow) == nil {
			return RouteRex
		}
	}
	if t.PaneID == "" {
		return RouteNone
	}
	return RouteTmux
}

// RunFocus performs the pane-existence check and then whatever FocusPlan
// decides. The impure check happens here, before the planner, so the planner
// stays pure — the repo's standard split.
//
// The focus REQUEST is written before the switch, and the order is load-bearing
// rather than incidental. The request is what makes the dashboard open and focus
// the named session; writing it first means the dashboard has already acted by the
// time switch-client brings the user's client over, so they land inside the
// session instead of watching it open under them. Reversed, the user would see the
// list for however long the write and the watcher's wakeup take.
//
// The switch does not depend on the request: a skipped or failed write costs the
// open+focus half of the click and leaves the pane switch intact.
//
// A click Rex answered writes no dashboard request: the request opens the session
// inside a dashboard's tmux pane, and on that route nobody is being switched to a
// dashboard to see it.
func RunFocus(outcome string, t Target, fr FocusRequest, focusRex FocusRexFunc) {
	// ClickRoute short-circuits before spending a tmux call: nothing about a
	// non-focus outcome or an unrecorded pane can produce a tmux action.
	if ClickRoute(outcome, t, focusRex) != RouteTmux {
		return
	}
	plan := FocusPlan(outcome, t, paneExists(t.PaneID), clientAttached(t.ClientTTY))
	if ShouldWriteFocusRequest(fr, len(plan) > 0) {
		writeFocusRequest(fr)
	}
	for _, a := range plan {
		runAction(a)
	}
}

// ShouldWriteFocusRequest decides whether a focus request should be published,
// given whether the click is actually going to switch the user's client. Pure, so
// the matrix is testable without a tmux server — the runner's own success path
// cannot be exercised in a test, because it really would move the tester's client.
//
// willSwitch gates it because the two halves of a click belong together: FocusPlan
// refuses a stale pane (the usual case for a toast clicked long after its
// dashboard died), and a request published for a click whose switch is not
// happening would make the dashboard yank focus with nothing bringing the user
// over to see it.
func ShouldWriteFocusRequest(fr FocusRequest, willSwitch bool) bool {
	if !willSwitch {
		return false
	}
	return fr.Session != "" && fr.GitCommonDir != "" && fr.TargetPID > 0
}

// writeFocusRequest publishes the "open and focus this session" request the
// dashboard watches for. Best-effort and silent: an unwritable request means the
// click still switches the pane, so there is nothing worth failing over — and
// nowhere to report it to, since this runs with stdio on /dev/null.
func writeFocusRequest(fr FocusRequest) {
	if fr.Session == "" || fr.GitCommonDir == "" || fr.TargetPID <= 0 {
		return
	}
	_ = dashmeta.WriteRequest(fr.GitCommonDir, fr.Session, fr.TargetPID, time.Now())
}

// runAlerter starts alerter, waits for its outcome, and returns it trimmed.
// Returns "" for every failure and for the WatchLifetime expiry, which
// FocusPlan then treats as do-nothing.
//
// The child gets Setsid so its process group id equals its pid, which is what
// makes the expiry path's Kill(-pid) a group kill. Setsid is set ALONE:
// SysProcAttr{Setsid: true, Setpgid: true} fails every spawn on darwin with
// "operation not permitted" (verified in W0.T1), and since this path must never
// fail a caller, that error would be swallowed into zero toasts with no
// diagnostic. Setsid already makes the child both a session leader and the
// leader of a new process group whose pgid is its pid, so the second field buys
// nothing.
func runAlerter(ctx context.Context, path string, args []string) string {
	cmd := exec.Command(path, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	// stdin/stderr on /dev/null; stdout is the outcome channel. Explicit
	// *os.File values keep exec from creating pipes and copier goroutines for
	// the streams we do not read.
	if null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0); err == nil {
		defer null.Close()
		cmd.Stdin = null
		cmd.Stderr = null
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return ""
	}
	if err := cmd.Start(); err != nil {
		return ""
	}

	read := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(stdout)
		read <- string(b)
	}()

	select {
	case s := <-read:
		_ = cmd.Wait()
		return strings.TrimSpace(s)
	case <-ctx.Done():
		// Kill the whole group rather than the process: alerter is the group
		// leader here, and anything it spawned must go with it.
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		// Wait closes the stdout pipe, which releases the reader goroutine.
		_ = cmd.Wait()
		return ""
	}
}

// paneExists asks tmux whether paneID is still a live pane anywhere on the
// server. Any failure (no server, timeout, tmux missing) reports false, which
// means "do nothing" — the safe direction, since the alternative is switching a
// client to a pane that may belong to someone else's work now.
func paneExists(paneID string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), tmuxTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, "tmux", "list-panes", "-a", "-F", "#{pane_id}").Output()
	if err != nil {
		return false
	}
	return PaneListed(string(out), paneID)
}

// clientAttached asks tmux whether clientTTY is still an attached client. Any
// failure reports false, meaning "do nothing" — the same safe direction
// paneExists takes, since the alternative is switching a client that is not the
// one the dashboard recorded.
func clientAttached(clientTTY string) bool {
	if clientTTY == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), tmuxTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, "tmux", "list-clients", "-F", "#{client_tty}").Output()
	if err != nil {
		return false
	}
	return ClientListed(string(out), clientTTY)
}

// runAction runs one planned command, bounded and best-effort. Errors are
// deliberately dropped: there is no one to report them to (stdio is /dev/null in
// the detached watcher) and the process must still exit 0.
func runAction(a Action) {
	ctx, cancel := context.WithTimeout(context.Background(), tmuxTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, a.Name, a.Args...)
	if null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0); err == nil {
		defer null.Close()
		cmd.Stdin = null
		cmd.Stdout = null
		cmd.Stderr = null
	}
	_ = cmd.Run()
}
