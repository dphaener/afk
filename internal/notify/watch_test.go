package notify

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/dashmeta"
)

// TestFocusPlan is the whole click matrix. Nothing here execs anything: alerter
// with no --timeout BLOCKS until a human interacts, so a test that ran it would
// hang until the go test timeout.
//
// Load-bearing rows:
//
//   - "empty TerminalApp" — switch ONLY. Empty means "switch the pane, skip
//     activation" (ResolveTarget's contract). Emitting an activate with an empty
//     app name would run `tell application "" to activate`, an AppleScript error
//     on every single click for users who never set AFK_TERMINAL_APP, which is
//     the default.
//   - "pane no longer exists" — NO actions. A persistent alert can be clicked
//     long after its pane died, and tmux reuses pane ids, so a stale-but-valid
//     id would silently teleport the user's client to unrelated work. Doing
//     nothing is the only safe answer.
//   - "@CLOSED" and "@TIMEOUT" — NO actions. These are the outcomes alerter
//     prints for dismissal and no-interaction. Treating either as a click would
//     yank the user's terminal for a notification they deliberately ignored.
//   - the quote/semicolon AFK_TERMINAL_APP rows — NO activate. That value is
//     the one string interpolated into script text rather than passed as its own
//     argv element, so a name that can close the string literal is an injection.
//   - "switch argv contains -c" (asserted for every switching row) — implicit
//     client resolution picks whatever single client is attached: wrong with
//     several, "no current client" with none.
func TestFocusPlan(t *testing.T) {
	full := Target{PaneID: "%42", ClientTTY: "/dev/ttys000", TerminalApp: "Ghostty"}

	tests := []struct {
		name       string
		outcome    string
		target     Target
		paneExists bool
		// clientExists defaults to false, so every row that expects actions must
		// set it: a recorded client tty that is no longer attached is as stale as a
		// dead pane, and FocusPlan refuses both.
		clientExists bool
		want         []Action
	}{
		{
			name:         "Focus click, pane exists, known terminal -> switch then activate",
			outcome:      "Focus",
			target:       full,
			paneExists:   true,
			clientExists: true,
			want: []Action{
				{Name: "tmux", Args: []string{"switch-client", "-c", "/dev/ttys000", "-t", "%42"}},
				{Name: "osascript", Args: []string{"-e", `tell application "Ghostty" to activate`}},
			},
		},
		{
			name:         "Focus click with a trailing newline (alerter's real stdout) -> still switches",
			outcome:      "Focus\n",
			target:       full,
			paneExists:   true,
			clientExists: true,
			want: []Action{
				{Name: "tmux", Args: []string{"switch-client", "-c", "/dev/ttys000", "-t", "%42"}},
				{Name: "osascript", Args: []string{"-e", `tell application "Ghostty" to activate`}},
			},
		},
		{
			name:         "@CONTENTCLICKED -> same plan as Focus",
			outcome:      "@CONTENTCLICKED",
			target:       full,
			paneExists:   true,
			clientExists: true,
			want: []Action{
				{Name: "tmux", Args: []string{"switch-client", "-c", "/dev/ttys000", "-t", "%42"}},
				{Name: "osascript", Args: []string{"-e", `tell application "Ghostty" to activate`}},
			},
		},
		{
			name:         "@ACTIONCLICKED -> same plan as Focus",
			outcome:      "@ACTIONCLICKED",
			target:       full,
			paneExists:   true,
			clientExists: true,
			want: []Action{
				{Name: "tmux", Args: []string{"switch-client", "-c", "/dev/ttys000", "-t", "%42"}},
				{Name: "osascript", Args: []string{"-e", `tell application "Ghostty" to activate`}},
			},
		},
		{
			name:         "empty TerminalApp -> switch only, no activate",
			outcome:      "Focus",
			target:       Target{PaneID: "%42", ClientTTY: "/dev/ttys000"},
			paneExists:   true,
			clientExists: true,
			want: []Action{
				{Name: "tmux", Args: []string{"switch-client", "-c", "/dev/ttys000", "-t", "%42"}},
			},
		},
		{
			name:         "pane no longer exists -> no actions at all",
			outcome:      "Focus",
			target:       full,
			paneExists:   false,
			clientExists: true,
			want:         nil,
		},
		{
			name:         "empty PaneID -> no actions",
			outcome:      "Focus",
			target:       Target{ClientTTY: "/dev/ttys000", TerminalApp: "Ghostty"},
			paneExists:   true,
			clientExists: true,
			want:         nil,
		},
		{
			name:         "empty ClientTTY -> no actions (switch-client must keep -c)",
			outcome:      "Focus",
			target:       Target{PaneID: "%42", TerminalApp: "Ghostty"},
			paneExists:   true,
			clientExists: true,
			want:         nil,
		},
		{
			name:         "@CLOSED -> no actions",
			outcome:      "@CLOSED",
			target:       full,
			paneExists:   true,
			clientExists: true,
			want:         nil,
		},
		{
			name:         "@TIMEOUT -> no actions",
			outcome:      "@TIMEOUT",
			target:       full,
			paneExists:   true,
			clientExists: true,
			want:         nil,
		},
		{
			name:         "empty outcome (12h cap fired, or alerter never ran) -> no actions",
			outcome:      "",
			target:       full,
			paneExists:   true,
			clientExists: true,
			want:         nil,
		},
		{
			name:         "unknown token -> no actions",
			outcome:      "@SOMETHINGNEW",
			target:       full,
			paneExists:   true,
			clientExists: true,
			want:         nil,
		},
		{
			name:         "lowercase focus is NOT the label alerter prints -> no actions",
			outcome:      "focus",
			target:       full,
			paneExists:   true,
			clientExists: true,
			want:         nil,
		},
		{
			name:         "AFK_TERMINAL_APP with a double quote -> switch only, activation refused",
			outcome:      "Focus",
			target:       Target{PaneID: "%42", ClientTTY: "/dev/ttys000", TerminalApp: "Ghostty\" to activate\ntell application \"Calculator"},
			paneExists:   true,
			clientExists: true,
			want: []Action{
				{Name: "tmux", Args: []string{"switch-client", "-c", "/dev/ttys000", "-t", "%42"}},
			},
		},
		{
			name:         "AFK_TERMINAL_APP with a semicolon -> switch only, activation refused",
			outcome:      "Focus",
			target:       Target{PaneID: "%42", ClientTTY: "/dev/ttys000", TerminalApp: "Ghostty; rm -rf /"},
			paneExists:   true,
			clientExists: true,
			want: []Action{
				{Name: "tmux", Args: []string{"switch-client", "-c", "/dev/ttys000", "-t", "%42"}},
			},
		},
		{
			name:         "AFK_TERMINAL_APP with a backslash -> switch only, activation refused",
			outcome:      "Focus",
			target:       Target{PaneID: "%42", ClientTTY: "/dev/ttys000", TerminalApp: `Ghost\ty`},
			paneExists:   true,
			clientExists: true,
			want: []Action{
				{Name: "tmux", Args: []string{"switch-client", "-c", "/dev/ttys000", "-t", "%42"}},
			},
		},
		{
			name:         "app name with spaces and a dot is accepted",
			outcome:      "Focus",
			target:       Target{PaneID: "%42", ClientTTY: "/dev/ttys000", TerminalApp: "iTerm 2.app"},
			paneExists:   true,
			clientExists: true,
			want: []Action{
				{Name: "tmux", Args: []string{"switch-client", "-c", "/dev/ttys000", "-t", "%42"}},
				{Name: "osascript", Args: []string{"-e", `tell application "iTerm 2.app" to activate`}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FocusPlan(tt.outcome, tt.target, tt.paneExists, tt.clientExists)
			assertActions(t, got, tt.want)

			for _, a := range got {
				if a.Name != "tmux" {
					continue
				}
				if !containsArg(a.Args, "-c") {
					t.Errorf("switch-client argv %v is missing -c: implicit client "+
						"resolution is wrong with several clients and errors with none", a.Args)
				}
			}
		})
	}
}

// TestFocusPlanActionOrder pins switch-before-activate. Raising the window first
// would show whatever pane the client was on and then jump, so the user sees the
// wrong session flash.
func TestFocusPlanActionOrder(t *testing.T) {
	got := FocusPlan("Focus", Target{
		PaneID: "%7", ClientTTY: "/dev/ttys001", TerminalApp: "Ghostty",
	}, true, true)
	if len(got) != 2 {
		t.Fatalf("FocusPlan() returned %d actions, want 2: %+v", len(got), got)
	}
	if got[0].Name != "tmux" || got[1].Name != "osascript" {
		t.Errorf("FocusPlan() order = [%s %s], want [tmux osascript]", got[0].Name, got[1].Name)
	}
}

// TestFocusPlanNeverShellsOut guards the no-shell rule against a future
// "convenience" refactor: user text (the message, a session name, an app name)
// must never reach a shell, so no planned command may be sh/bash/zsh, and no
// argv element may be a shell -c invocation.
func TestFocusPlanNeverShellsOut(t *testing.T) {
	got := FocusPlan("Focus", Target{
		PaneID: "%7", ClientTTY: "/dev/ttys001", TerminalApp: "Ghostty",
	}, true, true)
	for _, a := range got {
		switch a.Name {
		case "sh", "bash", "zsh", "/bin/sh", "/bin/bash":
			t.Errorf("FocusPlan() plans a shell: %+v", a)
		}
	}
}

// TestIsFocusOutcome pins the outcome vocabulary W0.T1 OBSERVED rather than the
// one alerter's docs imply. The critical row is the bare literal "Focus": with a
// single --actions value alerter prints the action LABEL, not @ACTIONCLICKED, so
// an implementation matching only the @-tokens would ignore every real click.
func TestIsFocusOutcome(t *testing.T) {
	tests := []struct {
		outcome string
		want    bool
	}{
		{"Focus", true},
		{"Focus\n", true},
		{"  Focus  ", true},
		{"@CONTENTCLICKED", true},
		{"@ACTIONCLICKED", true},
		{"@CLOSED", false},
		{"@TIMEOUT", false},
		{"", false},
		{"\n", false},
		{"focus", false},
		{"@REPLIED", false},
		{"Focus something else", false},
	}
	for _, tt := range tests {
		t.Run(strings.TrimSpace(tt.outcome), func(t *testing.T) {
			if got := IsFocusOutcome(tt.outcome); got != tt.want {
				t.Errorf("IsFocusOutcome(%q) = %v, want %v", tt.outcome, got, tt.want)
			}
		})
	}
}

// TestFocusable keeps the Focus button and FocusPlan in agreement. A toast that
// renders a button FocusPlan would refuse to act on is a button that does
// nothing when clicked — the worst of the available failures, because it looks
// like the feature is broken rather than absent.
func TestFocusable(t *testing.T) {
	tests := []struct {
		name   string
		target Target
		want   bool
	}{
		{"pane and client -> focusable", Target{PaneID: "%1", ClientTTY: "/dev/ttys000"}, true},
		{"no pane -> not focusable", Target{ClientTTY: "/dev/ttys000"}, false},
		{"no client tty -> not focusable", Target{PaneID: "%1"}, false},
		{"neither -> not focusable", Target{}, false},
		{"terminal app alone -> not focusable", Target{TerminalApp: "Ghostty"}, false},
		{"rex session and window -> focusable", Target{RexSession: "S", RexWindow: "W"}, true},
		{"rex session without window -> not focusable", Target{RexSession: "S"}, false},
		{"rex window without session -> not focusable", Target{RexWindow: "W"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Focusable(tt.target); got != tt.want {
				t.Errorf("Focusable(%+v) = %v, want %v", tt.target, got, tt.want)
			}
			// The agreement itself: whenever Focusable says yes, the click must
			// have somewhere to go — Rex when both ids are set, else FocusPlan
			// with a live pane must produce at least the switch.
			if tt.target.RexSession != "" && tt.target.RexWindow != "" {
				ok := func(string, string) error { return nil }
				if got := ClickRoute(OutcomeFocus, tt.target, ok); tt.want != (got == RouteRex) {
					t.Errorf("Focusable(%+v) = %v but ClickRoute = %v", tt.target, tt.want, got)
				}
				return
			}
			plan := FocusPlan("Focus", tt.target, true, true)
			if tt.want != (len(plan) > 0) {
				t.Errorf("Focusable(%+v) = %v but FocusPlan produced %d actions",
					tt.target, tt.want, len(plan))
			}
		})
	}
}

// TestClickRoute is where a click goes once the toast is answered. Nothing here
// execs anything: the Rex call is a fake that records what it was asked.
//
// Load-bearing rows:
//
//   - "rex answers" — Rex wins over a recorded tmux pane, and tmux is not
//     touched (the route is RouteRex, so RunFocus returns before any tmux call).
//   - "rex error falls back to tmux" — a Rex that is down or a window that is gone
//     must not lose the click when a pane is known.
//   - "no window id goes to tmux without asking Rex" — FocusWindow would refuse an
//     empty id anyway, but asking at all would spend the deadline for nothing.
//   - "dismissed toast never asks Rex" — @CLOSED must not raise any window.
func TestClickRoute(t *testing.T) {
	errDown := errors.New("rex is down")
	rexIDs := Target{RexSession: "S", RexWindow: "W"}
	both := Target{RexSession: "S", RexWindow: "W", PaneID: "%1", ClientTTY: "/dev/ttys000"}

	tests := []struct {
		name      string
		outcome   string
		target    Target
		rexErr    error
		nilRex    bool
		want      Route
		wantAsked bool
	}{
		{name: "rex answers", outcome: OutcomeFocus, target: both, want: RouteRex, wantAsked: true},
		{name: "rex answers with no pane", outcome: OutcomeContentClicked, target: rexIDs, want: RouteRex, wantAsked: true},
		{name: "rex error falls back to tmux", outcome: OutcomeFocus, target: both, rexErr: errDown, want: RouteTmux, wantAsked: true},
		{name: "rex error with no pane does nothing", outcome: OutcomeFocus, target: rexIDs, rexErr: errDown, want: RouteNone, wantAsked: true},
		{name: "no window id goes to tmux without asking Rex", outcome: OutcomeFocus,
			target: Target{RexSession: "S", PaneID: "%1", ClientTTY: "/dev/ttys000"}, want: RouteTmux},
		{name: "no rex func goes to tmux", outcome: OutcomeFocus, target: both, nilRex: true, want: RouteTmux},
		{name: "tmux only", outcome: OutcomeFocus, target: Target{PaneID: "%1", ClientTTY: "/dev/ttys000"}, want: RouteTmux},
		{name: "neither", outcome: OutcomeFocus, target: Target{}, want: RouteNone},
		{name: "dismissed toast never asks Rex", outcome: "@CLOSED", target: both, want: RouteNone},
		{name: "timed-out toast never asks Rex", outcome: "@TIMEOUT", target: both, want: RouteNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var asked [][2]string
			var focus FocusRexFunc = func(s, w string) error {
				asked = append(asked, [2]string{s, w})
				return tt.rexErr
			}
			if tt.nilRex {
				focus = nil
			}
			if got := ClickRoute(tt.outcome, tt.target, focus); got != tt.want {
				t.Errorf("ClickRoute() = %v, want %v", got, tt.want)
			}
			if (len(asked) > 0) != tt.wantAsked {
				t.Fatalf("Rex asked %d times, want asked=%v", len(asked), tt.wantAsked)
			}
			if tt.wantAsked && (len(asked) != 1 || asked[0] != [2]string{tt.target.RexSession, tt.target.RexWindow}) {
				t.Errorf("Rex asked %v, want once for %s/%s", asked, tt.target.RexSession, tt.target.RexWindow)
			}
		})
	}
}

// TestRunFocusRexRouteWritesNoRequest pins that a click Rex answered publishes no
// dashboard request: the request opens the session in a dashboard's tmux pane, and
// nothing is switching the owner to that dashboard. The Rex fake answers, so
// RunFocus returns before any tmux exec.
func TestRunFocusRexRouteWritesNoRequest(t *testing.T) {
	dir := t.TempDir()
	var asked int
	RunFocus(OutcomeFocus,
		Target{RexSession: "S", RexWindow: "W", PaneID: "%42", ClientTTY: "/dev/ttys000"},
		FocusRequest{Session: "s", GitCommonDir: dir, TargetPID: testDashPID},
		func(string, string) error { asked++; return nil })
	if asked != 1 {
		t.Errorf("Rex asked %d times, want 1", asked)
	}
	if _, found := dashmeta.ReadRequest(dir, testDashPID); found {
		t.Errorf("a dashboard request was published for a click Rex answered")
	}
}

// TestPaneListed pins EXACT per-line matching. The load-bearing row is "%1
// against a list containing %12": tmux hands out pane ids sequentially, so %1 and
// %12 routinely coexist, and a prefix match would switch the user to the wrong
// pane whenever a two-digit id exists.
func TestPaneListed(t *testing.T) {
	tests := []struct {
		name   string
		out    string
		paneID string
		want   bool
	}{
		{"present alone", "%42\n", "%42", true},
		{"present among several", "%1\n%12\n%42\n", "%42", true},
		{"absent", "%1\n%2\n", "%42", false},
		{"prefix collision: %1 must not match %12", "%12\n%13\n", "%1", false},
		{"suffix collision: %2 must not match %12", "%12\n", "%2", false},
		{"empty pane id is never listed", "%1\n%2\n", "", false},
		{"empty output", "", "%1", false},
		{"no trailing newline", "%1", "%1", true},
		{"windows-style line ending is trimmed", "%1\r\n", "%1", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PaneListed(tt.out, tt.paneID); got != tt.want {
				t.Errorf("PaneListed(%q, %q) = %v, want %v", tt.out, tt.paneID, got, tt.want)
			}
		})
	}
}

// TestValidTerminalApp is the injection guard for the one user string that gets
// interpolated into script text. Empty is invalid rather than "activate
// nothing": it is the documented signal for "skip activation".
func TestValidTerminalApp(t *testing.T) {
	tests := []struct {
		app  string
		want bool
	}{
		{"Ghostty", true},
		{"iTerm2", true},
		{"iTerm 2.app", true},
		{"Terminal", true},
		{"my-term_1.0", true},
		{"", false},
		{`Ghostty"`, false},
		{"Ghostty'", false},
		{"Ghostty; rm -rf /", false},
		{`Ghost\ty`, false},
		{"Ghostty\nCalculator", false},
		{"Ghostty$(whoami)", false},
		{"Ghostty`id`", false},
		{"Ghostty&", false},
	}
	for _, tt := range tests {
		t.Run(tt.app, func(t *testing.T) {
			if got := ValidTerminalApp(tt.app); got != tt.want {
				t.Errorf("ValidTerminalApp(%q) = %v, want %v", tt.app, got, tt.want)
			}
		})
	}
}

// TestActivateScript pins the AppleScript form. Only reached for names
// ValidTerminalApp accepted, which is why it does no escaping of its own.
func TestActivateScript(t *testing.T) {
	got := ActivateScript("Ghostty")
	want := `tell application "Ghostty" to activate`
	if got != want {
		t.Errorf("ActivateScript() = %q, want %q", got, want)
	}
}

// TestWatchLifetimeIsBounded pins that the cap exists and is a real duration.
// W0.T1 observed alerter processes resident past their own --timeout, and alerter
// with no --timeout blocks until a human acts, so an unbounded watcher leaks a
// process pair per ignored notification, forever.
func TestWatchLifetimeIsBounded(t *testing.T) {
	if WatchLifetime <= 0 {
		t.Fatalf("WatchLifetime = %v, want a positive bound", WatchLifetime)
	}
	if WatchLifetime > 24*time.Hour {
		t.Errorf("WatchLifetime = %v, longer than a day: an ignored toast would "+
			"hold two processes across more than one working session", WatchLifetime)
	}
}

func assertActions(t *testing.T, got, want []Action) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d actions %+v, want %d %+v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i].Name != want[i].Name {
			t.Errorf("action %d name = %q, want %q", i, got[i].Name, want[i].Name)
		}
		if len(got[i].Args) != len(want[i].Args) {
			t.Fatalf("action %d args = %v, want %v", i, got[i].Args, want[i].Args)
		}
		for j := range want[i].Args {
			if got[i].Args[j] != want[i].Args[j] {
				t.Errorf("action %d args = %v, want %v", i, got[i].Args, want[i].Args)
			}
		}
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// --- focus request (the open+focus half of a click) ---

// testDashPID is the dashboard a test's focus request is addressed to. Requests
// are per-dashboard so exactly one dashboard honors a click; the value only has to
// be a positive pid, since nothing here reads it back through a liveness check.
const testDashPID = 4242

// TestWriteFocusRequest is the request-write matrix. Load-bearing rows: both
// "missing" rows write NOTHING, because a request that cannot name a session, or
// cannot say where to publish it, is one the dashboard could only misread — and
// the switch-client half of the click still works without it, so refusing costs
// nothing. The success row is what makes the dashboard open and focus the named
// session at all.
func TestWriteFocusRequest(t *testing.T) {
	tests := []struct {
		name      string
		session   string
		useDir    bool
		wantWrite bool
	}{
		{name: "session and dir", session: "improve-desktop-notifications", useDir: true, wantWrite: true},
		{name: "no session name", session: "", useDir: true, wantWrite: false},
		{name: "no git common dir", session: "s", useDir: false, wantWrite: false},
		{name: "neither", session: "", useDir: false, wantWrite: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := ""
			probe := t.TempDir()
			if tt.useDir {
				dir = probe
			}

			writeFocusRequest(FocusRequest{Session: tt.session, GitCommonDir: dir, TargetPID: testDashPID})

			got, found := dashmeta.ReadRequest(probe, testDashPID)
			if found != tt.wantWrite {
				t.Fatalf("request written = %v, want %v", found, tt.wantWrite)
			}
			if tt.wantWrite && got.Name != tt.session {
				t.Errorf("request Name = %q, want %q", got.Name, tt.session)
			}
		})
	}
}

// TestWriteFocusRequestIsFresh pins that the stamp is written as "now" in the
// format the consumer parses. A request the dashboard reads as stale is a click
// that silently does half its job, and the two halves live in different packages,
// so nothing but this makes them agree.
func TestWriteFocusRequestIsFresh(t *testing.T) {
	dir := t.TempDir()
	writeFocusRequest(FocusRequest{Session: "s", GitCommonDir: dir, TargetPID: testDashPID})

	got, found := dashmeta.ReadRequest(dir, testDashPID)
	if !found {
		t.Fatal("expected a request to be written")
	}
	if !dashmeta.RequestFresh(got, time.Now(), dashmeta.RequestMaxAge) {
		t.Errorf("just-written request %+v does not read as fresh", got)
	}
}

// TestRunFocusWritesNoRequestWhenItWillNotSwitch keeps the two halves of a click
// together: a request must never be published for a click whose pane switch is not
// going to happen, or the dashboard would yank the user's focus with nothing
// bringing their client over to see it.
//
// Both rows return from RunFocus BEFORE any exec (the outcome/pane short-circuit),
// so nothing here runs alerter or tmux.
func TestRunFocusWritesNoRequestWhenItWillNotSwitch(t *testing.T) {
	tests := []struct {
		name    string
		outcome string
		paneID  string
	}{
		{name: "dismissed toast", outcome: "@CLOSED", paneID: "%42"},
		{name: "timed-out toast", outcome: "@TIMEOUT", paneID: "%42"},
		{name: "no pane recorded", outcome: OutcomeFocus, paneID: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			RunFocus(tt.outcome,
				Target{PaneID: tt.paneID, ClientTTY: "/dev/ttys000"},
				FocusRequest{Session: "s", GitCommonDir: dir, TargetPID: testDashPID}, nil)
			if _, found := dashmeta.ReadRequest(dir, testDashPID); found {
				t.Errorf("a request was published for an outcome that switches nothing")
			}
		})
	}
}

// TestShouldWriteFocusRequest is the publish decision, and the two willSwitch=false
// rows are the load-bearing ones: they are the whole reason the parameter exists.
// A stale pane makes FocusPlan produce nothing, and publishing a request for a
// click that is not switching anything would have the dashboard yank the user's
// focus with nothing bringing them over to see it — the half-broken outcome that is
// worse than doing nothing at all.
//
// The two "missing field" rows are the degraded-but-normal cases: no --plan reached
// the child, or the git common dir could not be resolved. Both must decline
// silently and leave the pane switch alone.
func TestShouldWriteFocusRequest(t *testing.T) {
	full := FocusRequest{Session: "s", GitCommonDir: "/g", TargetPID: testDashPID}

	tests := []struct {
		name       string
		fr         FocusRequest
		willSwitch bool
		want       bool
	}{
		{name: "switching, session and dir present", fr: full, willSwitch: true, want: true},
		{name: "not switching (stale pane) -> no request", fr: full, willSwitch: false, want: false},
		{name: "not switching, nothing to write anyway", fr: FocusRequest{}, willSwitch: false, want: false},
		{name: "switching but no session name", fr: FocusRequest{GitCommonDir: "/g"}, willSwitch: true, want: false},
		{name: "switching but no git common dir", fr: FocusRequest{Session: "s"}, willSwitch: true, want: false},
		{name: "switching with neither", fr: FocusRequest{}, willSwitch: true, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShouldWriteFocusRequest(tt.fr, tt.willSwitch); got != tt.want {
				t.Errorf("ShouldWriteFocusRequest(%+v, willSwitch=%v) = %v, want %v",
					tt.fr, tt.willSwitch, got, tt.want)
			}
		})
	}
}

// TestShouldWriteFocusRequestAgreesWithFocusPlan keeps the gate and the planner in
// step over the real outcome/target matrix. Asserting the two together is what
// makes "a request is published exactly when the client is being switched" a
// property rather than a coincidence of two independently-edited functions.
func TestShouldWriteFocusRequestAgreesWithFocusPlan(t *testing.T) {
	fr := FocusRequest{Session: "s", GitCommonDir: "/g", TargetPID: testDashPID}
	live := Target{PaneID: "%42", ClientTTY: "/dev/ttys000"}

	tests := []struct {
		name       string
		outcome    string
		target     Target
		paneExists bool
	}{
		{name: "focus click on a live pane", outcome: OutcomeFocus, target: live, paneExists: true},
		{name: "focus click on a dead pane", outcome: OutcomeFocus, target: live, paneExists: false},
		{name: "dismissed", outcome: "@CLOSED", target: live, paneExists: true},
		{name: "timed out", outcome: "@TIMEOUT", target: live, paneExists: true},
		{name: "no client tty", outcome: OutcomeFocus, target: Target{PaneID: "%42"}, paneExists: true},
		{name: "no pane id", outcome: OutcomeFocus, target: Target{ClientTTY: "/dev/ttys000"}, paneExists: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			switching := len(FocusPlan(tt.outcome, tt.target, tt.paneExists, true)) > 0
			if got := ShouldWriteFocusRequest(fr, switching); got != switching {
				t.Errorf("FocusPlan switches = %v but ShouldWriteFocusRequest = %v; "+
					"the request and the switch must be decided together", switching, got)
			}
		})
	}
}

// TestClientListed is the client-revalidation matcher. It exists because
// Target.ClientTTY is recorded ONCE at dashboard startup: by the time a persistent
// alert is clicked, that client may have detached, and tty paths are reused — so
// the recorded string can name a different client than the one the dashboard was
// running in. Switching that one yanks a window the user was doing something else
// in, the same hazard the pane-existence check exists to prevent.
//
// Load-bearing rows: the exact-match pair (/dev/ttys1 must not match /dev/ttys10,
// since ttys1 and ttys10 coexist routinely), and the empty needle, which must
// never match the blank line tmux's output ends with.
func TestClientListed(t *testing.T) {
	const attached = "/dev/ttys000\n/dev/ttys004\n"

	tests := []struct {
		name      string
		out       string
		clientTTY string
		want      bool
	}{
		{name: "attached client", out: attached, clientTTY: "/dev/ttys000", want: true},
		{name: "second attached client", out: attached, clientTTY: "/dev/ttys004", want: true},
		{name: "detached client", out: attached, clientTTY: "/dev/ttys009", want: false},
		{name: "no clients at all", out: "", clientTTY: "/dev/ttys000", want: false},
		{name: "prefix must not match", out: "/dev/ttys10\n", clientTTY: "/dev/ttys1", want: false},
		{name: "suffix must not match", out: "/dev/ttys1\n", clientTTY: "/dev/ttys10", want: false},
		{name: "empty tty is never listed", out: attached, clientTTY: "", want: false},
		{name: "trailing whitespace is tolerated", out: "  /dev/ttys000  \n", clientTTY: "/dev/ttys000", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClientListed(tt.out, tt.clientTTY); got != tt.want {
				t.Errorf("ClientListed(%q, %q) = %v, want %v", tt.out, tt.clientTTY, got, tt.want)
			}
		})
	}
}

// TestFocusPlanRefusesADetachedClient is the client half of the staleness contract,
// stated on its own because it is a distinct failure from a dead pane: the pane can
// still exist while the client that was viewing it is gone, and switching a client
// that is no longer attached either errors or moves someone else's window.
func TestFocusPlanRefusesADetachedClient(t *testing.T) {
	live := Target{PaneID: "%42", ClientTTY: "/dev/ttys000", TerminalApp: "Ghostty"}

	if got := FocusPlan(OutcomeFocus, live, true, false); got != nil {
		t.Errorf("FocusPlan with a detached client planned %+v, want no actions", got)
	}
	if got := FocusPlan(OutcomeFocus, live, true, true); len(got) == 0 {
		t.Error("FocusPlan with a live pane AND a live client must still plan the switch")
	}
}

// TestShouldWriteFocusRequestNeedsATargetDashboard pins the pid requirement on the
// publish gate. A request with no target dashboard cannot be addressed to anyone —
// ReadRequest looks for request.<pid>.json — so writing one would leave a file that
// no dashboard ever reads and only the stale sweep ever collects.
func TestShouldWriteFocusRequestNeedsATargetDashboard(t *testing.T) {
	tests := []struct {
		name      string
		targetPID int
		want      bool
	}{
		{name: "a live dashboard was found", targetPID: 4242, want: true},
		{name: "no dashboard entry (pid 0)", targetPID: 0, want: false},
		{name: "negative pid", targetPID: -1, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fr := FocusRequest{Session: "s", GitCommonDir: "/g", TargetPID: tt.targetPID}
			if got := ShouldWriteFocusRequest(fr, true); got != tt.want {
				t.Errorf("ShouldWriteFocusRequest(TargetPID=%d) = %v, want %v", tt.targetPID, got, tt.want)
			}
		})
	}
}

// TestWriteFocusRequestNeedsATargetDashboard is the writer's own refusal, separate
// from the gate above so removing either one fails: writeFocusRequest is called
// only behind ShouldWriteFocusRequest today, but it is the function that actually
// touches the disk.
func TestWriteFocusRequestNeedsATargetDashboard(t *testing.T) {
	dir := t.TempDir()
	writeFocusRequest(FocusRequest{Session: "s", GitCommonDir: dir, TargetPID: 0})

	entries, err := os.ReadDir(dashmeta.Dir(dir))
	if err == nil && len(entries) > 0 {
		t.Errorf("afk-dash/ contains %d file(s) for a request with no target dashboard", len(entries))
	}
}
