package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/dphaener/afk/internal/dashmeta"
	"github.com/dphaener/afk/internal/host"
	"github.com/dphaener/afk/internal/notify"
	"github.com/dphaener/afk/internal/rex"
)

// notifyInternalFlags is the set of flags that cross the process boundary from
// `afk notify` to its detached `afk notify --watch` child. It is written out
// literally, from the task's canonical list, rather than derived from either side
// — a list derived from the code it is checking could not catch a rename.
//
// `sound` is deliberately NOT here. notify.Format never populates Toast.Sound
// (the name is per-backend and AlerterArgs/OsascriptArgs each hardcode their
// own), so the flag was always empty and always ignored, and it is gone from both
// sides. Re-adding it to this list without a parent that populates it makes
// TestNotifySpawnArgsMatchInternalFlags red, which is the intent.
// `session` IS here: it is what a honored click names to the dashboard, and it
// crosses the process boundary exactly like the rest. Without it the child would
// switch the pane and have nothing to ask the dashboard to open — the half-done
// click this contract exists to make impossible.
// `dash-pid` IS here for the same reason at one remove: it names WHICH dashboard
// the click is addressed to. Drop it and the child publishes to pid 0, which no
// dashboard reads, so every click switches the pane and lands on the list.
var notifyInternalFlags = []string{
	"watch", "backend", "title", "subtitle", "body",
	"group", "pane", "client-tty", "terminal-app", "session", "dash-pid",
	"rex-session", "rex-window",
}

var notifyPublicFlags = []string{"plan", "repo", "message", "dry-run", "check"}

// TestNotifySpawnArgsMatchInternalFlags IS THE GUARD AGAINST THE SILENT
// ZERO-TOAST MISMATCH, and it is the reason the spawn and the FlagSet live in one
// file owned by one task.
//
// The two halves are a wire protocol between two runs of the same binary, with no
// type shared between them: notifySpawnArgs writes flag NAMES as strings, and the
// child's FlagSet parses them. Rename either side alone and nothing fails to
// compile, no test about behavior breaks, and the only symptom is that every
// notification silently stops appearing — flag.ExitOnError in a process whose
// stderr is /dev/null exits 2 with no output anywhere. This test asserts the two
// sets are EXACTLY equal in both directions, so a drift on either side is a red
// test rather than a dead feature.
func TestNotifySpawnArgsMatchInternalFlags(t *testing.T) {
	spawned := flagNamesIn(notifySpawnArgs(
		notify.Toast{Title: "t", Subtitle: "s", Body: "b", Group: "g"},
		notify.BackendAlerter,
		notify.Target{PaneID: "%1", ClientTTY: "/dev/ttys000", TerminalApp: "Ghostty", RexSession: "S", RexWindow: "W"},
		"a-session",
		4242,
	))
	registered := registeredNotifyFlags(t)

	// Every flag the spawn emits must be one the child registered, or the child
	// exits 2 on "flag provided but not defined" with its stderr on /dev/null.
	for _, name := range spawned {
		if !registered[name] {
			t.Errorf("notifySpawnArgs emits --%s but cmdNotify's FlagSet does not register it: "+
				"the detached child would exit 2 with no diagnostic and no toast would ever appear", name)
		}
	}

	// And every internal flag the child registers must be one the spawn emits,
	// or that half of the toast silently arrives empty.
	spawnedSet := map[string]bool{}
	for _, name := range spawned {
		spawnedSet[name] = true
	}
	for _, name := range notifyInternalFlags {
		if !registered[name] {
			t.Errorf("cmdNotify's FlagSet is missing the canonical internal flag --%s", name)
		}
		if !spawnedSet[name] {
			t.Errorf("notifySpawnArgs never emits the internal flag --%s, so the child "+
				"always sees its zero value", name)
		}
	}

	sort.Strings(spawned)
	want := append([]string(nil), notifyInternalFlags...)
	sort.Strings(want)
	if strings.Join(spawned, ",") != strings.Join(want, ",") {
		t.Errorf("spawn argv flag names = %v, want exactly %v", spawned, want)
	}
}

// TestNotifyFlagContractIsTheCanonicalNames pins the literal, canonical flag
// names on the FlagSet side. notify.sh (W4.T1) calls the public ones by name from
// a shell script, where a rename is not a compile error anywhere, so the public
// set needs a name-level assertion of its own.
func TestNotifyFlagContractIsTheCanonicalNames(t *testing.T) {
	registered := registeredNotifyFlags(t)
	for _, name := range notifyPublicFlags {
		if !registered[name] {
			t.Errorf("cmdNotify's FlagSet is missing the public flag --%s, which notify.sh calls by name", name)
		}
	}
	for _, name := range notifyInternalFlags {
		if !registered[name] {
			t.Errorf("cmdNotify's FlagSet is missing the internal flag --%s", name)
		}
	}
	// Nothing beyond the two canonical sets: an undeclared flag here is a flag
	// the task's contract does not cover and that no other side knows about.
	allowed := map[string]bool{}
	for _, n := range append(append([]string(nil), notifyPublicFlags...), notifyInternalFlags...) {
		allowed[n] = true
	}
	for name := range registered {
		if !allowed[name] {
			t.Errorf("cmdNotify registers --%s, which is in neither canonical set; "+
				"add it to the contract deliberately or drop it", name)
		}
	}
}

// TestNotifyUsageOmitsInternalFlags mirrors printUsage's treatment of `_host`:
// the spawn's wire flags are an implementation detail, and documenting them would
// invite a user to call --watch by hand, which blocks for up to 12h.
func TestNotifyUsageOmitsInternalFlags(t *testing.T) {
	usage := captureNotifyUsage(t)
	for _, name := range notifyInternalFlags {
		if strings.Contains(usage, "--"+name) {
			t.Errorf("afk notify usage documents the internal flag --%s:\n%s", name, usage)
		}
	}
	for _, name := range notifyPublicFlags {
		if !strings.Contains(usage, "--"+name) {
			t.Errorf("afk notify usage never mentions the public flag --%s:\n%s", name, usage)
		}
	}
}

// TestNotifyRepoName is the group-collision guard. The load-bearing rows are the
// two worktree ones: a afk worktree's cwd basename is
// `<repo>.<session>`, so deriving the repo from the cwd (rather than from the git
// COMMON dir, which every worktree of a repo shares) would put the session name
// inside the repo token and defeat `afk-{repo}-{plan}`'s whole purpose — two
// same-named sessions in different repos would replace each other's toasts.
func TestNotifyRepoName(t *testing.T) {
	tests := []struct {
		name         string
		gitCommonDir string
		want         string
	}{
		{
			name:         "main checkout: .git parent is the repo",
			gitCommonDir: "/Users/x/ontra/afk/.git",
			want:         "afk",
		},
		{
			name:         "worktree resolves to the SAME repo, not the worktree dir",
			gitCommonDir: "/Users/x/ontra/afk/.git",
			want:         "afk",
		},
		{
			name:         "trailing separator is cleaned before the basename",
			gitCommonDir: "/Users/x/ontra/afk/.git/",
			want:         "afk",
		},
		{
			name:         "bare or separate git dir: use the basename with .git trimmed",
			gitCommonDir: "/Users/x/mirrors/afk.git",
			want:         "afk",
		},
		{
			name:         "empty common dir (git failed or not a repo) -> empty repo",
			gitCommonDir: "",
			want:         "",
		},
		{
			name:         "repo at the filesystem root has no parent name",
			gitCommonDir: "/.git",
			want:         "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := notifyRepoName(tt.gitCommonDir); got != tt.want {
				t.Errorf("notifyRepoName(%q) = %q, want %q", tt.gitCommonDir, got, tt.want)
			}
		})
	}
}

// TestNotifyRepoNameIgnoresWorktreeSuffix is the specific regression this
// function exists for, stated as its own test so it cannot be lost in a table
// edit: the worktree directory this code was written in is
// `afk.improve-desktop-notifications`, and the repo name must be
// `afk`, with no session suffix anywhere in it.
func TestNotifyRepoNameIgnoresWorktreeSuffix(t *testing.T) {
	worktreeCommonDir := filepath.Join("/Users/x/ontra/afk", ".git")
	got := notifyRepoName(worktreeCommonDir)
	if got != "afk" {
		t.Fatalf("notifyRepoName(%q) = %q, want %q", worktreeCommonDir, got, "afk")
	}
	if strings.Contains(got, ".") {
		t.Errorf("repo name %q still carries a worktree suffix: the group id would "+
			"collapse repo and session into one token", got)
	}
}

// TestNotifyDryRunLines asserts fields INDIVIDUALLY rather than as one output
// block. Pane, client-tty and terminal-app depend on whether a dashboard happens
// to be running and on the developer's env, so a whole-block assertion would be
// green or red depending on the machine.
func TestNotifyDryRunLines(t *testing.T) {
	toast := notify.Format(notify.Input{
		PlanName: "improve-desktop-notifications",
		Repo:     "afk",
		Message:  "needs input",
	})
	target := notify.Target{PaneID: "%42", ClientTTY: "/dev/ttys000", TerminalApp: "Ghostty"}
	lines := notifyDryRunLines(toast, notify.BackendAlerter, true, target)

	for _, want := range []struct{ prefix, value string }{
		{"title:", "afk · afk"},
		{"subtitle:", "improve-desktop-notifications"},
		{"body:", "needs input"},
		{"group:", "afk-afk-improve-desktop-notifications"},
		{"sound:", "default"},
		{"backend:", "alerter"},
		{"pane:", "%42"},
		{"client-tty:", "/dev/ttys000"},
		{"terminal-app:", "Ghostty"},
	} {
		assertFieldLine(t, lines, want.prefix, want.value)
	}
}

// TestNotifyDryRunLinesWithNoNotifier covers the machine with nothing installed:
// --dry-run still reports the full toast (the formatting is backend-independent
// and is what the user is checking) and says plainly that there is no backend.
func TestNotifyDryRunLinesWithNoNotifier(t *testing.T) {
	toast := notify.Format(notify.Input{PlanName: "p", Repo: "r", Message: "m"})
	lines := notifyDryRunLines(toast, "", false, notify.Target{})

	assertFieldLine(t, lines, "backend:", "(none)")
	assertFieldLine(t, lines, "sound:", "(none)")
	assertFieldLine(t, lines, "pane:", "(none)")
	assertFieldLine(t, lines, "client-tty:", "(none)")
	assertFieldLine(t, lines, "terminal-app:", "(none)")
	assertFieldLine(t, lines, "title:", "afk · r")
}

// TestNotifyDryRunSound reports the sound the chosen backend would really use.
// Toast.Sound is empty by design (notify.Format leaves it so, because the name is
// per-backend), and printing that empty field would tell the user "no sound",
// which is wrong for both backends.
func TestNotifyDryRunSound(t *testing.T) {
	tests := []struct {
		name    string
		backend notify.Backend
		found   bool
		want    string
	}{
		{"alerter takes the literal default", notify.BackendAlerter, true, "default"},
		{"osascript needs Ping (no default.aiff exists)", notify.BackendOsascript, true, "Ping"},
		{"no notifier", "", false, "(none)"},
		{"unknown backend name", notify.Backend("mystery"), true, "(none)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := notifyDryRunSound(tt.backend, tt.found); got != tt.want {
				t.Errorf("notifyDryRunSound(%q, %v) = %q, want %q", tt.backend, tt.found, got, tt.want)
			}
		})
	}
}

// TestNotifyCheckLines pins --check's two load-bearing strings. The Settings
// entry must name TERMINAL, never alerter: W0.T1 established that alerter
// delivers under com.apple.Terminal's identity and that overriding the sender
// suppresses delivery entirely, so Terminal is the only entry whose style the
// user can usefully change. Telling them to look for "alerter" in System Settings
// sends them hunting for a row that does not exist.
func TestNotifyCheckLines(t *testing.T) {
	tests := []struct {
		name        string
		backend     notify.Backend
		found       bool
		wantInstall bool
	}{
		{"alerter present -> no install pitch", notify.BackendAlerter, true, false},
		{"osascript only -> pitch alerter (osascript can never report a click)", notify.BackendOsascript, true, true},
		{"nothing found -> pitch alerter", "", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := strings.Join(notifyCheckLines(tt.backend, tt.found), "\n")

			if strings.Contains(out, notifyInstallCmd) != tt.wantInstall {
				t.Errorf("notifyCheckLines(%q, %v) install-command presence = %v, want %v:\n%s",
					tt.backend, tt.found, !tt.wantInstall, tt.wantInstall, out)
			}
			if !strings.Contains(out, "Terminal") {
				t.Errorf("notifyCheckLines(%q, %v) never names the Terminal Settings entry:\n%s",
					tt.backend, tt.found, out)
			}
			if !strings.Contains(out, "Alerts") {
				t.Errorf("notifyCheckLines(%q, %v) never mentions the Alerts style:\n%s",
					tt.backend, tt.found, out)
			}
			if strings.Contains(out, "Notifications > alerter") {
				t.Errorf("notifyCheckLines(%q, %v) points at an alerter Settings entry, "+
					"which does not exist — alerter delivers as Terminal:\n%s", tt.backend, tt.found, out)
			}
		})
	}
}

// TestNotifyCheckMakesNoUnverifiedPersistenceClaim pins the ONE hedge --check's
// text has to keep. Whether the Alerts style makes these toasts persist was never
// observed — System Settings cannot be driven programmatically, so only the user
// can confirm it — and `com.apple.Terminal` is absent from this machine's
// com.apple.ncprefs.plist even though delivery works, so the row may not even be
// there to toggle.
//
// The rows are the two shapes the claim came back as: an imperative that promises
// the outcome ("set X" / "then set X", the wording this replaced), and any sentence
// asserting persistence as a consequence. Both must stay out, and the
// unverified-marker assertion is what stops a future edit from deleting the caveat
// and leaving a bare instruction behind.
func TestNotifyCheckMakesNoUnverifiedPersistenceClaim(t *testing.T) {
	banned := []string{
		"For toasts that wait to be clicked, set",
		"Then set ",
		"to persist",
		"will persist",
		"makes toasts persist",
	}
	for _, tt := range []struct {
		name    string
		backend notify.Backend
		found   bool
	}{
		{"alerter present", notify.BackendAlerter, true},
		{"osascript only", notify.BackendOsascript, true},
		{"nothing found", "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out := strings.Join(notifyCheckLines(tt.backend, tt.found), "\n")
			for _, phrase := range banned {
				if strings.Contains(out, phrase) {
					t.Errorf("--check states %q, promising an outcome nobody observed:\n%s", phrase, out)
				}
			}
			if !strings.Contains(out, "unverified") {
				t.Errorf("--check names the Settings step without marking it unverified:\n%s", out)
			}
		})
	}
}

// TestNotifyCheckReportsTheBackendItFound keeps --check's text honest about
// which backend is present, since "notifier: alerter" vs "notifier: osascript" is
// how the user learns whether clicking a toast can work at all.
func TestNotifyCheckReportsTheBackendItFound(t *testing.T) {
	alerter := strings.Join(notifyCheckLines(notify.BackendAlerter, true), "\n")
	if !strings.Contains(alerter, "alerter") {
		t.Errorf("notifyCheckLines(alerter, true) does not name alerter:\n%s", alerter)
	}
	osa := strings.Join(notifyCheckLines(notify.BackendOsascript, true), "\n")
	if !strings.Contains(osa, "osascript") {
		t.Errorf("notifyCheckLines(osascript, true) does not name osascript:\n%s", osa)
	}
	none := strings.Join(notifyCheckLines("", false), "\n")
	if !strings.Contains(none, "none") {
		t.Errorf("notifyCheckLines(\"\", false) does not say none was found:\n%s", none)
	}
}

// TestNotifySpawnArgsRoutesBackToNotify pins the subcommand name at the head of
// the child argv. Without it the re-exec would land on bare `afk`, which on a
// TTY launches the dashboard — a notification hook that seizes a terminal.
func TestNotifySpawnArgsRoutesBackToNotify(t *testing.T) {
	args := notifySpawnArgs(notify.Toast{}, notify.BackendAlerter, notify.Target{}, "", 0)
	if len(args) == 0 || args[0] != "notify" {
		t.Fatalf("notifySpawnArgs()[0] = %q, want %q", firstOrEmpty(args), "notify")
	}
	if r := routeArgs(args, true); r.kind != routeSubcommand || r.name != "notify" {
		t.Errorf("routeArgs(spawn argv) = {kind:%v name:%q}, want {routeSubcommand notify}", r.kind, r.name)
	}
}

// TestNotifySpawnArgsPassesValuesVerbatim pins that user text crosses the process
// boundary as its own argv element, unescaped and unquoted. A message with a
// quote, a newline or a leading dash must arrive byte-identical, which is only
// true because nothing here goes through a shell.
func TestNotifySpawnArgsPassesValuesVerbatim(t *testing.T) {
	toast := notify.Toast{
		Title:    `afk · re"po`,
		Subtitle: "plan\nwith-newline",
		Body:     `--not-a-flag; rm -rf /`,
		Group:    "afk-r-p",
	}
	args := notifySpawnArgs(toast, notify.BackendAlerter, notify.Target{}, "", 0)

	for _, want := range []struct{ flag, value string }{
		{"--title", toast.Title},
		{"--subtitle", toast.Subtitle},
		{"--body", toast.Body},
		{"--group", toast.Group},
	} {
		got, ok := valueAfter(args, want.flag)
		if !ok {
			t.Fatalf("notifySpawnArgs() has no %s", want.flag)
		}
		if got != want.value {
			t.Errorf("%s = %q, want %q (verbatim)", want.flag, got, want.value)
		}
	}
}

// TestNotifySpawnArgvIsWellFormed is the guard the original flag-contract test
// could not be: that one compared flag NAMES and passed while every value was
// broken. A live spawn was inspected with `ps` and showed `--sound` and
// `--terminal-app` each holding nothing, the latter as the LAST argv element —
// which in `ps` is indistinguishable from a flag that swallowed the token after
// it, and under Go's flag package (flag.go's parseOne takes f.args[0] as the
// value with no dash check) would silently annex any argument appended below it
// later.
//
// The rows are the two shapes that occur in production, and BOTH are
// load-bearing. "dashboard running" is the verified-working path. "no dash entry"
// is the ordinary case on a machine with no dashboard up: pane and client-tty are
// both empty there, so they must be ABSENT, and it is the row that would have
// failed on the original code.
func TestNotifySpawnArgvIsWellFormed(t *testing.T) {
	toast := notify.Format(notify.Input{PlanName: "p", Repo: "r", Message: "m"})
	tests := []struct {
		name        string
		target      notify.Target
		session     string
		dashPID     int
		wantAbsent  []string
		wantPresent []string
	}{
		{
			name:        "dash entry found, AFK_TERMINAL_APP unset (the normal case)",
			target:      notify.Target{PaneID: "%61", ClientTTY: "/dev/ttys000"},
			session:     "a-session",
			dashPID:     4242,
			wantAbsent:  []string{"terminal-app"},
			wantPresent: []string{"backend", "title", "subtitle", "body", "group", "pane", "client-tty", "session", "dash-pid"},
		},
		{
			name:        "no dash entry: pane, client-tty and dash-pid must be absent, not empty",
			target:      notify.Target{},
			wantAbsent:  []string{"pane", "client-tty", "terminal-app", "session", "dash-pid"},
			wantPresent: []string{"backend", "title", "subtitle", "body", "group"},
		},
		{
			name:        "everything populated",
			target:      notify.Target{PaneID: "%61", ClientTTY: "/dev/ttys000", TerminalApp: "Ghostty"},
			session:     "a-session",
			dashPID:     4242,
			wantPresent: []string{"backend", "title", "subtitle", "body", "group", "pane", "client-tty", "terminal-app", "session", "dash-pid"},
		},
		{
			name:        "no plan name: session must be absent, not empty",
			target:      notify.Target{PaneID: "%61", ClientTTY: "/dev/ttys000"},
			dashPID:     4242,
			wantAbsent:  []string{"session", "terminal-app"},
			wantPresent: []string{"backend", "title", "subtitle", "body", "group", "pane", "client-tty"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := notifySpawnArgs(toast, notify.BackendAlerter, tt.target, tt.session, tt.dashPID)

			for i, a := range args {
				if a == "" {
					t.Errorf("notifySpawnArgs()[%d] is an empty string; full argv %q", i, args)
				}
			}

			pairs := notifySpawnPairs(t, args)
			for _, name := range tt.wantAbsent {
				if _, ok := pairs[name]; ok {
					t.Errorf("--%s carries an empty value and must be omitted entirely; full argv %q", name, args)
				}
			}
			for _, name := range tt.wantPresent {
				v, ok := pairs[name]
				if !ok {
					t.Errorf("--%s is missing from the spawn argv %q", name, args)
					continue
				}
				if v == "" {
					t.Errorf("--%s is present with an empty value; full argv %q", name, args)
				}
			}
		})
	}
}

// TestNotifySpawnArgsHasNoSoundFlag pins the removal. Toast.Sound is never
// populated (notify.Format leaves it so, because the name is per-backend and
// AlerterArgs/OsascriptArgs each hardcode their own), so a --sound on the wire
// could only ever carry "". Re-adding it without a parent that fills it in is the
// regression: the flag reads as live, the next reader trusts it in an argv
// builder, and the toasts go silent with nothing to point at.
func TestNotifySpawnArgsHasNoSoundFlag(t *testing.T) {
	args := notifySpawnArgs(
		notify.Toast{Title: "t", Subtitle: "s", Body: "b", Group: "g", Sound: "ignored"},
		notify.BackendAlerter,
		notify.Target{PaneID: "%1", ClientTTY: "/dev/ttys000"},
		"a-session",
		4242,
	)
	for _, a := range args {
		if a == "--sound" {
			t.Errorf("notifySpawnArgs() emits --sound, which nothing populates and nothing reads: %q", args)
		}
	}
	if registeredNotifyFlags(t)["sound"] {
		t.Error("cmdNotify's FlagSet still registers --sound; the child has no use for a value the parent never sets")
	}
}

// TestNotifySpawnArgsChildParsesItBack closes the loop the name-comparison test
// leaves open: it feeds the real spawn argv to the real FlagSet and asserts every
// value arrives. An omitted flag must be indistinguishable from an empty one on
// this side — that equivalence is what makes omitting safe — and a dangling flag
// would surface here as a parse error instead of silently at runtime in a process
// whose stderr is /dev/null.
func TestNotifySpawnArgsChildParsesItBack(t *testing.T) {
	toast := notify.Format(notify.Input{PlanName: "plan", Repo: "repo", Message: `--not-a-flag; rm -rf /`})
	args := notifySpawnArgs(toast, notify.BackendAlerter,
		notify.Target{PaneID: "%61", ClientTTY: "/dev/ttys000"}, "plan", 4242)

	var f notifyFlags
	fs := flag.NewFlagSet("notify", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	defineNotifyFlags(fs, &f)
	// args[0] is the subcommand name main.go strips before handing off.
	if err := fs.Parse(args[1:]); err != nil {
		t.Fatalf("the child cannot parse its own spawn argv %q: %v", args, err)
	}
	if fs.NArg() != 0 {
		t.Errorf("spawn argv leaves %d positional args %q, which cmdNotify rejects with exit 2",
			fs.NArg(), fs.Args())
	}

	if !f.watch {
		t.Error("--watch did not arrive; the child would deliver nothing and fall through to the parent path")
	}
	for _, want := range []struct{ name, got, value string }{
		{"backend", f.backend, "alerter"},
		{"title", f.title, toast.Title},
		{"subtitle", f.subtitle, toast.Subtitle},
		{"body", f.body, toast.Body},
		{"group", f.group, toast.Group},
		{"pane", f.pane, "%61"},
		{"client-tty", f.clientTTY, "/dev/ttys000"},
		{"session", f.session, "plan"},
	} {
		if want.got != want.value {
			t.Errorf("child parsed --%s as %q, want %q", want.name, want.got, want.value)
		}
	}
	// Omitted, so it must arrive as the FlagSet's own default.
	if f.terminalApp != "" {
		t.Errorf("child parsed --terminal-app as %q, want %q from an omitted flag", f.terminalApp, "")
	}
}

// notifySpawnPairs parses the spawn argv STRUCTURALLY — positionally, in
// name/value pairs — rather than by scanning for a "--" prefix. Position is the
// only correct way to read it: --body legitimately carries values that begin with
// a dash (a message can be anything), so a prefix scan would misread the value as
// a flag and the following flag as its value. It fails the test on a dangling
// name with no value, which is the defect class this exists for.
func notifySpawnPairs(t *testing.T, args []string) map[string]string {
	t.Helper()
	if len(args) < 2 || args[0] != "notify" || args[1] != "--watch" {
		t.Fatalf("spawn argv must begin with `notify --watch`, got %q", args)
	}
	pairs := map[string]string{}
	for i := 2; i < len(args); i += 2 {
		name := args[i]
		if !strings.HasPrefix(name, "--") {
			t.Fatalf("spawn argv[%d] = %q is in a flag-name position but is not a flag; full argv %q",
				i, name, args)
		}
		if i+1 >= len(args) {
			t.Fatalf("spawn argv ends with the valueless flag %q: Go's flag package would take "+
				"whatever gets appended after it as its value; full argv %q", name, args)
		}
		pairs[strings.TrimPrefix(name, "--")] = args[i+1]
	}
	return pairs
}

// registeredNotifyFlags observes the flag names cmdNotify's FlagSet really
// carries, by running the same defineNotifyFlags call cmdNotify runs. Going
// through that function rather than a hand-copied list is what makes the
// flag-contract test load-bearing: a copy would keep passing after a rename.
func registeredNotifyFlags(t *testing.T) map[string]bool {
	t.Helper()
	fs := flag.NewFlagSet("notify", flag.ContinueOnError)
	defineNotifyFlags(fs, &notifyFlags{})
	names := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) { names[f.Name] = true })
	return names
}

func captureNotifyUsage(t *testing.T) string {
	t.Helper()
	var sb strings.Builder
	notifyUsage(&sb)
	return sb.String()
}

func flagNamesIn(args []string) []string {
	var names []string
	for _, a := range args {
		if strings.HasPrefix(a, "--") {
			names = append(names, strings.TrimPrefix(a, "--"))
		}
	}
	return names
}

func valueAfter(args []string, flagName string) (string, bool) {
	for i, a := range args {
		if a == flagName && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

func firstOrEmpty(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func assertFieldLine(t *testing.T, lines []string, prefix, want string) {
	t.Helper()
	for _, line := range lines {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		got := strings.TrimSpace(strings.TrimPrefix(line, prefix))
		if got != want {
			t.Errorf("%s line = %q, want value %q", prefix, got, want)
		}
		return
	}
	t.Errorf("no line starting with %q in:\n%s", prefix, strings.Join(lines, "\n"))
}

// TestNotifySpawnArgsOmitsDashPIDWhenNoDashboard pins the pid's omit-if-absent
// behavior against the "no empty argv element, ever" invariant. A `--dash-pid 0`
// would be worse than absent: it names a dashboard that cannot exist, and the child
// would publish a request nobody reads.
func TestNotifySpawnArgsOmitsDashPIDWhenNoDashboard(t *testing.T) {
	toast := notify.Format(notify.Input{PlanName: "p", Repo: "r", Message: "m"})

	for _, pid := range []int{0, -1} {
		args := notifySpawnArgs(toast, notify.BackendAlerter,
			notify.Target{PaneID: "%1", ClientTTY: "/dev/ttys000"}, "s", pid)
		for i, a := range args {
			if a == "--dash-pid" {
				t.Errorf("dashPID=%d emitted --dash-pid %q; it must be omitted entirely",
					pid, valueAt(args, i+1))
			}
		}
	}

	args := notifySpawnArgs(toast, notify.BackendAlerter,
		notify.Target{PaneID: "%1", ClientTTY: "/dev/ttys000"}, "s", 4242)
	if got, ok := valueAfter(args, "--dash-pid"); !ok || got != "4242" {
		t.Errorf("--dash-pid = %q (present=%v), want \"4242\"", got, ok)
	}
}

// TestDryRunHasNoSideEffects is the diagnostic-purity guarantee. --dry-run is what
// a user reaches for when click-to-focus is misbehaving, so it must be safe to run
// repeatedly against a suspect afk-dash/ and report the same thing every time.
//
// The concrete regression it pins: the dry-run path used to call
// dashmeta.ReadEntry, whose dead-pid self-heal DELETES entries as it scans. So
// inspecting the state destroyed part of it, and a second --dry-run answered
// differently from the first.
//
// Both halves are asserted: the directory is byte-for-byte unchanged, and the
// reported target still names the live dashboard (a "fix" that skipped the read
// entirely would satisfy the first check while making the command useless).
func TestDryRunHasNoSideEffects(t *testing.T) {
	gitCommonDir := t.TempDir()
	dir, err := dashmeta.EnsureDir(gitCommonDir)
	if err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}

	// A live dashboard (this process) and a dead one, which is exactly the state
	// ReadEntry's self-heal would prune.
	live := dashmeta.Entry{PID: os.Getpid(), Tmux: "/tmp/tmux-501/default,1,0", PaneID: "%61", ClientTTY: "/dev/ttys000"}
	writeTestEntry(t, dir, live)
	dead := dashmeta.Entry{PID: 999999, Tmux: "/tmp/tmux-501/default,1,0", PaneID: "%99", ClientTTY: "/dev/ttys009"}
	writeTestEntry(t, dir, dead)

	before := dirSnapshot(t, dir)

	entry, found := dashmeta.PeekEntry(gitCommonDir)
	target := notify.ResolveTarget(entry, found, func(string) string { return "" })
	lines := notifyDryRunLines(notify.Format(notify.Input{PlanName: "p", Repo: "r", Message: "m"}),
		notify.BackendAlerter, true, target)

	if got := dirSnapshot(t, dir); got != before {
		t.Errorf("--dry-run changed afk-dash/:\n before %s\n after  %s", before, got)
	}
	if !found || target.PaneID != "%61" {
		t.Errorf("--dry-run reported pane %q (found=%v), want the live dashboard's %%61 — "+
			"a side-effect-free dry run must still report the real target", target.PaneID, found)
	}
	if !containsLineWith(lines, "%61") {
		t.Errorf("--dry-run output does not mention the resolved pane:\n%s", strings.Join(lines, "\n"))
	}
}

func writeTestEntry(t *testing.T, dir string, e dashmeta.Entry) {
	t.Helper()
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshaling entry: %v", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("%d.json", e.PID))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("writing entry: %v", err)
	}
}

// dirSnapshot renders a directory's names and contents, so an assertion can be
// "nothing changed" rather than a list of things that might have.
func dirSnapshot(t *testing.T, dir string) string {
	t.Helper()
	names, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var out []string
	for _, e := range names {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("ReadFile %s: %v", e.Name(), err)
		}
		out = append(out, e.Name()+"="+string(data))
	}
	sort.Strings(out)
	return strings.Join(out, "|")
}

func containsLineWith(lines []string, want string) bool {
	for _, l := range lines {
		if strings.Contains(l, want) {
			return true
		}
	}
	return false
}

func valueAt(args []string, i int) string {
	if i < 0 || i >= len(args) {
		return ""
	}
	return args[i]
}

// TestNotifyWatchInputsCarriesEveryFlag is the guard the flag-contract test
// cannot be: that test compares flag NAMES on both sides of the wire, so a flag
// registered, emitted, and parsed but never mapped onto the struct the watch
// path acts on passes it cleanly. Load-bearing rows are dash-pid (whose omission
// made ShouldWriteFocusRequest refuse every write, silently killing
// click-to-focus while the suite stayed green) and session, since a request with
// no session name names nothing to open.
func TestNotifyWatchInputsCarriesEveryFlag(t *testing.T) {
	f := notifyFlags{
		watch:       true,
		backend:     "alerter",
		title:       "afk · repo",
		subtitle:    "session",
		body:        "needs input",
		group:       "afk-repo-session",
		pane:        "%61",
		clientTTY:   "/dev/ttys000",
		terminalApp: "Ghostty",
		session:     "session",
		dashPID:     4242,
		rexSession:  "rex-session-id",
		rexWindow:   "rex-window-id",
	}

	toast, target, request := notifyWatchInputs(f, "/repo/.git")

	for _, tc := range []struct{ field, got, want string }{
		{"Toast.Title", toast.Title, f.title},
		{"Toast.Subtitle", toast.Subtitle, f.subtitle},
		{"Toast.Body", toast.Body, f.body},
		{"Toast.Group", toast.Group, f.group},
		{"Target.PaneID", target.PaneID, f.pane},
		{"Target.ClientTTY", target.ClientTTY, f.clientTTY},
		{"Target.TerminalApp", target.TerminalApp, f.terminalApp},
		{"Target.RexSession", target.RexSession, f.rexSession},
		{"Target.RexWindow", target.RexWindow, f.rexWindow},
		{"FocusRequest.Session", request.Session, f.session},
		{"FocusRequest.GitCommonDir", request.GitCommonDir, "/repo/.git"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q — the flag was parsed but never reached the watch path", tc.field, tc.got, tc.want)
		}
	}

	if request.TargetPID != f.dashPID {
		t.Errorf("FocusRequest.TargetPID = %d, want %d — ShouldWriteFocusRequest requires >0, so a zero here means no request is ever published and a click leaves the user on the dashboard list instead of inside the session",
			request.TargetPID, f.dashPID)
	}

	if toast.Sound != "" {
		t.Errorf("Toast.Sound = %q, want empty: there is no --sound flag on the wire because each backend's argv builder supplies its own name", toast.Sound)
	}
}

// writeNotifyHostJSON writes .afk/<name>/host.json under root, the file
// notifyRexFacts reads.
func writeNotifyHostJSON(t *testing.T, root, name string, m host.Meta) {
	t.Helper()
	if err := host.WriteMeta(root, name, m); err != nil {
		t.Fatal(err)
	}
}

const (
	notifyMacClients = `{"clients":[{"client_id":"c1","connected_at":"2026-10-28T10:00:00Z","connection_state":"connected","info":{"kind":"app","platform":"macos","label":"Mac"}}]}`
	notifyIOSClients = `{"clients":[{"client_id":"c2","connected_at":"2026-10-28T10:00:00Z","connection_state":"connected","info":{"kind":"app","platform":"ios","label":"iPhone"}}]}`
	notifyLostMac    = `{"clients":[{"client_id":"c1","connected_at":"2026-10-28T10:00:00Z","connection_state":"lost","info":{"kind":"app","platform":"macos","label":"Mac"}}]}`
)

// TestNotifyRexFacts is the impure read behind the notice decision, run against
// temp directories and a rex.Fake; no rex binary is run.
//
// Load-bearing rows:
//
//   - "client.list error fails open" — Rex down must read as "no Mac", or a
//     waiting agent's only notice would be skipped (decision D4).
//   - "only an iOS client" and "mac lost" — rule 1 may drop afk's toast only for a
//     connected Rex for Mac, the one client known to raise its own notice.
//   - "not waiting asks Rex nothing" and "no rex block asks Rex nothing" — the
//     parent is on a hook's critical path, so Rex is asked only when the answer
//     can change the decision.
//   - "host.json in the worktree" — a session's host.json lives in its own
//     worktree, not under the git common dir's parent.
func TestNotifyRexFacts(t *testing.T) {
	block := &host.RexBlock{SessionID: "S1", WindowID: "W1", BlockID: "B1"}
	blocked := &host.AgentStatus{State: "blocked", Kind: "approval", At: "2026-10-28T10:00:00Z"}
	working := &host.AgentStatus{State: "working", At: "2026-10-28T10:00:00Z"}

	tests := []struct {
		name        string
		meta        *host.Meta // nil: no host.json at all
		inWorktree  bool
		plan        string
		clients     string
		clientsErr  error
		wantBlock   bool
		wantWaiting bool
		wantMac     bool
		wantCalls   int
	}{
		{name: "no host.json", plan: "p", wantCalls: 0},
		{name: "no plan name", meta: &host.Meta{Rex: block, AgentStatus: blocked}, plan: "", wantCalls: 0},
		{name: "no rex block asks Rex nothing", meta: &host.Meta{PID: 1, AgentStatus: blocked}, plan: "p", wantCalls: 0},
		{name: "block present and Mac connected", meta: &host.Meta{Rex: block, AgentStatus: blocked}, plan: "p",
			clients: notifyMacClients, wantBlock: true, wantWaiting: true, wantMac: true, wantCalls: 1},
		{name: "host.json in the worktree", meta: &host.Meta{Rex: block, AgentStatus: blocked}, inWorktree: true, plan: "p",
			clients: notifyMacClients, wantBlock: true, wantWaiting: true, wantMac: true, wantCalls: 1},
		{name: "only an iOS client", meta: &host.Meta{Rex: block, AgentStatus: blocked}, plan: "p",
			clients: notifyIOSClients, wantBlock: true, wantWaiting: true, wantCalls: 1},
		{name: "mac lost", meta: &host.Meta{Rex: block, AgentStatus: blocked}, plan: "p",
			clients: notifyLostMac, wantBlock: true, wantWaiting: true, wantCalls: 1},
		{name: "client.list error fails open", meta: &host.Meta{Rex: block, AgentStatus: blocked}, plan: "p",
			clientsErr: errors.New("rex: no server"), wantBlock: true, wantWaiting: true, wantCalls: 1},
		{name: "not waiting asks Rex nothing", meta: &host.Meta{Rex: block, AgentStatus: working}, plan: "p",
			wantBlock: true, wantCalls: 0},
		{name: "no agent status asks Rex nothing", meta: &host.Meta{Rex: block}, plan: "p", wantBlock: true, wantCalls: 0},
		{name: "block without window asks Rex nothing", meta: &host.Meta{Rex: &host.RexBlock{SessionID: "S1", BlockID: "B1"}, AgentStatus: blocked},
			plan: "p", wantBlock: true, wantWaiting: true, wantCalls: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkout := t.TempDir()
			worktree := t.TempDir()
			gitCommonDir := filepath.Join(checkout, ".git")
			if tt.meta != nil {
				root := checkout
				if tt.inWorktree {
					root = worktree
				}
				writeNotifyHostJSON(t, root, "p", *tt.meta)
			}
			fake := &rex.Fake{}
			if tt.clients != "" || tt.clientsErr != nil {
				fake.OnArgs("client.list", map[string]any{"kinds": []string{"app"}}, tt.clients, tt.clientsErr)
			}

			gotBlock, gotWaiting, gotMac := notifyRexFacts(context.Background(), fake, worktree, gitCommonDir, tt.plan)

			if (gotBlock != nil) != tt.wantBlock {
				t.Fatalf("rex block = %+v, want present=%v", gotBlock, tt.wantBlock)
			}
			if tt.wantBlock && gotBlock.SessionID != "S1" {
				t.Errorf("rex block session = %q, want S1", gotBlock.SessionID)
			}
			if gotWaiting != tt.wantWaiting {
				t.Errorf("waiting = %v, want %v", gotWaiting, tt.wantWaiting)
			}
			if gotMac != tt.wantMac {
				t.Errorf("macConnected = %v, want %v", gotMac, tt.wantMac)
			}
			if calls := fake.Calls(); len(calls) != tt.wantCalls {
				t.Errorf("Rex calls = %v, want %d", calls, tt.wantCalls)
			}
		})
	}
}

// TestNotifyChoose maps the facts onto notify.ChooseNotice and the watcher's
// target. Load-bearing rows: "waiting with Mac" is the only row with no toast;
// "rex and tmux" keeps the pane as the watcher's fallback while the Rex ids carry
// the choice; "block without window" must not set the Rex ids, since an empty
// window id would ask Rex to clear the active window.
func TestNotifyChoose(t *testing.T) {
	tmux := notify.Target{PaneID: "%1", ClientTTY: "/dev/ttys000"}
	block := &host.RexBlock{SessionID: "S1", WindowID: "W1", BlockID: "B1"}

	tests := []struct {
		name        string
		target      notify.Target
		block       *host.RexBlock
		waiting     bool
		mac         bool
		wantToast   bool
		wantRexIDs  bool
		wantPane    string
		wantFocusTm bool
	}{
		{name: "waiting with Mac", target: tmux, block: block, waiting: true, mac: true, wantToast: false, wantRexIDs: true, wantPane: "%1"},
		{name: "waiting without Mac", target: tmux, block: block, waiting: true, wantToast: true, wantRexIDs: true, wantPane: "%1"},
		{name: "rex and tmux", target: tmux, block: block, wantToast: true, wantRexIDs: true, wantPane: "%1"},
		{name: "rex only", block: block, mac: true, wantToast: true, wantRexIDs: true},
		{name: "tmux only", target: tmux, waiting: true, mac: true, wantToast: true, wantPane: "%1", wantFocusTm: true},
		{name: "neither", wantToast: true},
		{name: "block without window", target: tmux, block: &host.RexBlock{SessionID: "S1", BlockID: "B1"}, waiting: true, mac: true,
			wantToast: true, wantPane: "%1", wantFocusTm: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action, got := notifyChoose(tt.target, tt.block, tt.waiting, tt.mac)
			if action.Toast != tt.wantToast {
				t.Errorf("Toast = %v, want %v (%s)", action.Toast, tt.wantToast, action.Reason)
			}
			if action.FocusTmux != tt.wantFocusTm {
				t.Errorf("FocusTmux = %v, want %v", action.FocusTmux, tt.wantFocusTm)
			}
			hasIDs := got.RexSession == "S1" && got.RexWindow == "W1"
			if hasIDs != tt.wantRexIDs || (!tt.wantRexIDs && (got.RexSession != "" || got.RexWindow != "")) {
				t.Errorf("Rex ids = %q/%q, want set=%v", got.RexSession, got.RexWindow, tt.wantRexIDs)
			}
			if got.PaneID != tt.wantPane {
				t.Errorf("PaneID = %q, want %q", got.PaneID, tt.wantPane)
			}
		})
	}
}

// TestNotifyRexDryRunLines pins that --dry-run names the Rex window a click would
// bring forward, and says plainly when the toast is skipped.
func TestNotifyRexDryRunLines(t *testing.T) {
	block := &host.RexBlock{SessionID: "S1", WindowID: "W1", BlockID: "B1"}
	action, target := notifyChoose(notify.Target{PaneID: "%1"}, block, false, false)
	joined := strings.Join(notifyRexDryRunLines(action, target, block, false, false), "\n")
	for _, want := range []string{"rex-session:  S1", "rex-window:   W1", "toast:        yes", "click:        Rex window W1 in session S1 (tmux pane %1 if Rex cannot)"} {
		if !strings.Contains(joined, want) {
			t.Errorf("dry-run lines missing %q:\n%s", want, joined)
		}
	}

	action, target = notifyChoose(notify.Target{}, block, true, true)
	joined = strings.Join(notifyRexDryRunLines(action, target, block, true, true), "\n")
	if !strings.Contains(joined, "toast:        no") {
		t.Errorf("dry-run lines do not say the toast is skipped:\n%s", joined)
	}

	action, target = notifyChoose(notify.Target{}, nil, false, false)
	joined = strings.Join(notifyRexDryRunLines(action, target, nil, false, false), "\n")
	for _, want := range []string{"rex-session:  (none)", "click:        nothing"} {
		if !strings.Contains(joined, want) {
			t.Errorf("dry-run lines missing %q:\n%s", want, joined)
		}
	}
}
