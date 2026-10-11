package main

import (
	"os"
	"strings"
	"testing"
)

// TestRouteArgs is the full dispatch matrix. The rows that carry real weight:
//
//   - ["--interval","5s"] is a usage ERROR, not the dashboard. This is the row
//     that pins the invariant that `afk dash`'s flags never became implicit
//     top-level flags. Asserting the routing decision (not just "is this the
//     default action") is what makes the row non-tautological.
//   - Empty/nil args with stdoutTTY=false is a usage error, not the dashboard.
//     Bubble Tea only ever checks stdin and falls back to /dev/tty, so an
//     ungated bare `afk` with stdout piped starts invisibly and wedges the
//     terminal in raw mode instead of failing. See routeArgs' doc comment.
//   - nil vs []string{} both route identically: guards a len(nil) regression.
//   - stdoutTTY is irrelevant once a subcommand is named — `afk dash | cat`
//     must still reach cmdDash, which owns its own failure modes.
//   - `notify` with stdout PIPED must still dispatch. Its only real caller is a
//     Claude Code hook, which has no terminal at all, so a stdout gate on this
//     name would mean zero notifications in exactly the case the command exists
//     for.
//   - `inbox` with stdout PIPED must dispatch for the same reason: `afk inbox
//     add` is typed by an agent with no terminal, and its words and flags must
//     reach cmdInbox untouched.
//   - []string{""} (`afk ""`) is routeUnknownSubcommand with an EMPTY name, not
//     routeUsageError and not the dashboard. An earlier revision used a
//     `name != ""` sentinel to decide whether to print the diagnostic, which
//     silently dropped the `Unknown subcommand: ` line for this input. The empty
//     name is why the diagnostic needs its own route kind.
func TestRouteArgs(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		stdoutTTY bool
		wantKind  routeKind
		wantName  string
		wantArgs  []string
	}{
		{
			name:      "empty args on a terminal -> dashboard",
			args:      []string{},
			stdoutTTY: true,
			wantKind:  routeDash,
		},
		{
			name:      "nil args on a terminal -> dashboard (guards len(nil))",
			args:      nil,
			stdoutTTY: true,
			wantKind:  routeDash,
		},
		{
			name:      "empty args, stdout not a terminal -> usage error, no dashboard",
			args:      []string{},
			stdoutTTY: false,
			wantKind:  routeUsageError,
		},
		{
			name:      "nil args, stdout not a terminal -> usage error",
			args:      nil,
			stdoutTTY: false,
			wantKind:  routeUsageError,
		},
		{
			name:      "leading flag --interval -> unknown subcommand (dash flags are not top-level)",
			args:      []string{"--interval", "5s"},
			stdoutTTY: true,
			wantKind:  routeUnknownSubcommand,
			wantName:  "--interval",
		},
		{
			name:      "leading flag --interval, stdout piped -> still unknown subcommand",
			args:      []string{"--interval", "5s"},
			stdoutTTY: false,
			wantKind:  routeUnknownSubcommand,
			wantName:  "--interval",
		},
		{
			name:      "unknown subcommand -> usage error naming it",
			args:      []string{"frobnicate"},
			stdoutTTY: true,
			wantKind:  routeUnknownSubcommand,
			wantName:  "frobnicate",
		},
		{
			name:      `empty-string subcommand (afk "") -> diagnostic with an empty name`,
			args:      []string{""},
			stdoutTTY: true,
			wantKind:  routeUnknownSubcommand,
			wantName:  "",
		},
		{
			name:      `empty-string subcommand, stdout piped -> still the diagnostic`,
			args:      []string{""},
			stdoutTTY: false,
			wantKind:  routeUnknownSubcommand,
			wantName:  "",
		},
		{
			name:      "--help -> usage, exit 0",
			args:      []string{"--help"},
			stdoutTTY: true,
			wantKind:  routeUsage,
		},
		{
			name:      "-h -> usage, exit 0",
			args:      []string{"-h"},
			stdoutTTY: true,
			wantKind:  routeUsage,
		},
		{
			name:      "help -> usage, exit 0",
			args:      []string{"help"},
			stdoutTTY: true,
			wantKind:  routeUsage,
		},
		{
			name:      "--help with stdout piped -> still usage, exit 0",
			args:      []string{"--help"},
			stdoutTTY: false,
			wantKind:  routeUsage,
		},
		{
			name:      "inbox with stdout piped -> subcommand, args passed through",
			args:      []string{"inbox", "add", "--kind", "notice", "--title", "x", "--no-block"},
			stdoutTTY: false,
			wantKind:  routeSubcommand,
			wantName:  "inbox",
			wantArgs:  []string{"add", "--kind", "notice", "--title", "x", "--no-block"},
		},
		{
			name:      "dash -> subcommand, no args",
			args:      []string{"dash"},
			stdoutTTY: true,
			wantKind:  routeSubcommand,
			wantName:  "dash",
			wantArgs:  []string{},
		},
		{
			name:      "dash with stdout piped -> still dispatches to cmdDash",
			args:      []string{"dash"},
			stdoutTTY: false,
			wantKind:  routeSubcommand,
			wantName:  "dash",
			wantArgs:  []string{},
		},
		{
			name:      "dash --interval 5s -> subcommand carrying its flags",
			args:      []string{"dash", "--interval", "5s"},
			stdoutTTY: true,
			wantKind:  routeSubcommand,
			wantName:  "dash",
			wantArgs:  []string{"--interval", "5s"},
		},
		{
			name:      "new with a name -> subcommand carrying the name",
			args:      []string{"new", "my-session"},
			stdoutTTY: true,
			wantKind:  routeSubcommand,
			wantName:  "new",
			wantArgs:  []string{"my-session"},
		},
		{
			name:      "clean --force name -> subcommand carrying flag and name",
			args:      []string{"clean", "--force", "my-session"},
			stdoutTTY: true,
			wantKind:  routeSubcommand,
			wantName:  "clean",
			wantArgs:  []string{"--force", "my-session"},
		},
		{
			name:      "attach name -> subcommand",
			args:      []string{"attach", "my-session"},
			stdoutTTY: true,
			wantKind:  routeSubcommand,
			wantName:  "attach",
			wantArgs:  []string{"my-session"},
		},
		{
			name:      "notify with its public flags -> subcommand carrying them",
			args:      []string{"notify", "--plan", "my-session", "--message", "needs input"},
			stdoutTTY: true,
			wantKind:  routeSubcommand,
			wantName:  "notify",
			wantArgs:  []string{"--plan", "my-session", "--message", "needs input"},
		},
		{
			name:      "notify with stdout piped -> still dispatches (hooks have no TTY)",
			args:      []string{"notify", "--plan", "my-session"},
			stdoutTTY: false,
			wantKind:  routeSubcommand,
			wantName:  "notify",
			wantArgs:  []string{"--plan", "my-session"},
		},
		{
			name:      "_host stays routable though undocumented",
			args:      []string{"_host", "--session", "my-session"},
			stdoutTTY: true,
			wantKind:  routeSubcommand,
			wantName:  "_host",
			wantArgs:  []string{"--session", "my-session"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := routeArgs(tt.args, tt.stdoutTTY)
			if got.kind != tt.wantKind {
				t.Errorf("routeArgs(%v, %v).kind = %v, want %v",
					tt.args, tt.stdoutTTY, got.kind, tt.wantKind)
			}
			if got.name != tt.wantName {
				t.Errorf("routeArgs(%v, %v).name = %q, want %q",
					tt.args, tt.stdoutTTY, got.name, tt.wantName)
			}
			if len(got.args) != len(tt.wantArgs) {
				t.Fatalf("routeArgs(%v, %v).args = %v, want %v",
					tt.args, tt.stdoutTTY, got.args, tt.wantArgs)
			}
			for i := range tt.wantArgs {
				if got.args[i] != tt.wantArgs[i] {
					t.Fatalf("routeArgs(%v, %v).args = %v, want %v",
						tt.args, tt.stdoutTTY, got.args, tt.wantArgs)
				}
			}
		})
	}
}

// TestRouteSubcommandNamesAreDispatchable closes the gap a name list next to a
// switch would leave open: every name routeArgs resolves to routeSubcommand
// must have a non-nil entry to call, or main would panic on a valid command.
func TestRouteSubcommandNamesAreDispatchable(t *testing.T) {
	for name, fn := range subcommands {
		if fn == nil {
			t.Errorf("subcommands[%q] is nil", name)
		}
		if got := routeArgs([]string{name}, true); got.kind != routeSubcommand || got.name != name {
			t.Errorf("routeArgs([%q], true) = {kind:%v name:%q}, want {routeSubcommand %q}",
				name, got.kind, got.name, name)
		}
	}
}

// TestUnhandledRouteFailsClosed pins the two properties that together stop an
// unhandled route from opening the dashboard. main's switch has a default case
// that exits 1, but that case is only reachable if routeDash is not what a
// zero-valued route decays to — otherwise `route{}` would land on `case
// routeDash:` and seize the terminal on what is by definition a routing bug.
func TestUnhandledRouteFailsClosed(t *testing.T) {
	if routeKind(0) != routeInvalid {
		t.Errorf("routeKind zero value = %v, want routeInvalid", routeKind(0))
	}
	if (route{}).kind == routeDash {
		t.Error("a zero-valued route decays to routeDash: main would launch the dashboard on an unset kind")
	}
}

// TestRouteArgsNeverReturnsInvalid keeps routeInvalid unreachable from real
// input: it exists only as a fail-closed zero value, so any argument list
// resolving to it would mean main printing "internal error" to a user.
func TestRouteArgsNeverReturnsInvalid(t *testing.T) {
	corpus := [][]string{
		nil, {}, {""}, {"-h"}, {"--help"}, {"help"}, {"dash"}, {"new", "x"},
		{"clean"}, {"attach", "x"}, {"_host"}, {"--interval", "5s"}, {"frobnicate"},
		{"-"}, {"--"}, {"Dash"}, {" dash"},
	}
	for _, stdoutTTY := range []bool{true, false} {
		for _, args := range corpus {
			if got := routeArgs(args, stdoutTTY); got.kind == routeInvalid {
				t.Errorf("routeArgs(%q, %v).kind = routeInvalid", args, stdoutTTY)
			}
		}
	}
}

// TestPrintUsageDocumentsEverySubcommand guards drift between the two lists that
// have to agree: a name added to subcommands without a printUsage line would
// otherwise ship undiscoverable. `_host` is the one deliberate exception — it is
// the internal session-host entry point and is intentionally undocumented, so it
// is named here explicitly rather than the guard being loosened to "some names
// may be missing".
func TestPrintUsageDocumentsEverySubcommand(t *testing.T) {
	undocumented := map[string]bool{"_host": true}

	got := capturePrintUsage(t)
	for name := range subcommands {
		if undocumented[name] {
			if strings.Contains(got, name) {
				t.Errorf("printUsage() documents %q, which is meant to stay internal; "+
					"remove it from the undocumented set if that changed deliberately", name)
			}
			continue
		}
		if !strings.Contains(got, name) {
			t.Errorf("subcommands has %q but printUsage() never mentions it:\n%s", name, got)
		}
	}
}

// TestPrintUsageDocumentsDefaultAction pins the substantive tokens of the
// default-action line rather than its column alignment, so reflowing the usage
// block cannot fail a test about semantics.
func TestPrintUsageDocumentsDefaultAction(t *testing.T) {
	got := capturePrintUsage(t)
	for _, want := range []string{"afk", "Launch the dashboard", "afk dash"} {
		if !strings.Contains(got, want) {
			t.Errorf("printUsage() output missing %q:\n%s", want, got)
		}
	}
}

// TestPrintUsageKeepsSubcommandList guards the default-action line being added
// by replacing the usage block rather than extending it.
func TestPrintUsageKeepsSubcommandList(t *testing.T) {
	got := capturePrintUsage(t)
	for _, want := range []string{"Usage:", "Subcommands:", "new <name>", "dash", "clean [name]", "attach <name>", "Session model:"} {
		if !strings.Contains(got, want) {
			t.Errorf("printUsage() output missing pre-existing content %q:\n%s", want, got)
		}
	}
}

// capturePrintUsage swaps os.Stderr for a temp file rather than a pipe: a pipe
// would deadlock if the usage text ever outgrew the kernel buffer, since
// printUsage writes before anything reads.
func capturePrintUsage(t *testing.T) string {
	t.Helper()

	f, err := os.CreateTemp(t.TempDir(), "usage-*.txt")
	if err != nil {
		t.Fatalf("os.CreateTemp() failed: %v", err)
	}
	defer f.Close()

	old := os.Stderr
	os.Stderr = f
	defer func() { os.Stderr = old }()

	printUsage()

	out, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatalf("reading captured stderr failed: %v", err)
	}
	return string(out)
}
