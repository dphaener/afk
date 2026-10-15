package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dphaener/afk/internal/daemonconfig"
	"github.com/dphaener/afk/internal/dashmeta"
	"github.com/dphaener/afk/internal/project"
	"github.com/dphaener/afk/internal/ui"
	"github.com/dphaener/afk/internal/uistate"
)

var errSentinel = errors.New("dashboard failed")

func pidJSON() string { return fmt.Sprintf("%d.json", os.Getpid()) }

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// dashEntryModel builds the real ui.Model `afk dash` runs for loc, with the
// two scan seams cleared.
//
// Everything else comes from dashDeps — the same assembly runDashProgram uses —
// so a value dash.go neglects to thread shows up as a failure here rather than as
// a silent absence in the field. Scan and LoadConfig are the only exceptions:
// left wired, Init would read the developer's own ~/.config/afk/config.toml
// and walk whatever search roots it names (see ui.Deps.Scan on why nil is
// inert), and these tests are about the entry, not the scan. The empty repo
// suppresses the per-project discovery Cmds for the same reason.
func dashEntryModel(loc dashLocation) ui.Model {
	deps := dashDeps("", loc, nil, -1)
	deps.Scan, deps.LoadConfig = nil, nil
	return ui.New("", time.Millisecond, deps)
}

// dispatchInitBatch runs cmd and fires the members of the one tea.BatchMsg it
// produces, each on its own goroutine — the way Bubble Tea's event loop does.
//
// Concurrency is not an optimization here: Init's batch carries the focus-request
// watch subscription, which blocks on its channel until an event arrives or the
// watcher closes, so a sequential drain would never reach the entry write beside
// it. Messages are discarded; the side effect is the subject.
func dispatchInitBatch(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("Init returned no Cmd at all")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		return
	}
	for _, c := range batch {
		if c == nil {
			continue
		}
		go func(c tea.Cmd) { _ = c() }(c)
	}
}

// entrySettle bounds the wait below. The write is a same-process file write
// dispatched on a goroutine, so it lands in microseconds; the budget is generous
// because a loaded machine can schedule that goroutine late, and it is spent in
// full only by the rows that expect nothing to be written.
const entrySettle = 250 * time.Millisecond

// waitForEntry polls ReadEntry until an entry appears, or entrySettle expires.
//
// It returns the moment one exists and otherwise spends the whole budget, which
// is what makes "a dashboard outside tmux writes NOTHING" an assertion rather
// than a race: a single immediate read would pass even against a write that
// lands 10ms later.
func waitForEntry(t *testing.T, gitCommonDir string) (dashmeta.Entry, bool) {
	t.Helper()
	deadline := time.Now().Add(entrySettle)
	for {
		entry, found := dashmeta.ReadEntry(gitCommonDir)
		if found {
			return entry, true
		}
		if time.Now().After(deadline) {
			return entry, false
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// quitDash drives the dashboard's quit: ctrl+] then q. A bare q only raises
// a notice now that every action runs behind the leader key, so a single
// keypress would leave the Model (and its dashmeta entry) alive. Driving Update
// is how this package reaches the Model's removal path, which lives in an
// unexported method it cannot call.
//
// The leader's armed state lives in the returned model, so the second press
// goes to that model, not the original.
//
// Text is set on q because that is how a real printable press arrives under
// Bubble Tea v2 and what Key.String() returns for key.Matches.
func quitDash(m tea.Model) {
	m, _ = m.Update(tea.KeyPressMsg{Code: ']', Mod: tea.ModCtrl})
	_, _ = m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
}

// TestDashUsageNamesTheLeader pins the dashboard's key help to the leader
// model in internal/ui: every action runs after ctrl+], and the list keys
// are the ones that still act without it. The stale rows are the old action
// keys — a user who reads `ctrl+t` or `n new` here presses a key that now
// does nothing. The leader rows are checked one by one, not as a block, so a
// reworded line still fails if its key is missing.
func TestDashUsageNamesTheLeader(t *testing.T) {
	fs := flag.NewFlagSet("dash", flag.ContinueOnError)
	var out bytes.Buffer
	dashUsage(&out, fs)
	got := out.String()

	for _, want := range []string{
		"preview running session in agent pane",
		"enter      focus the right side",
		"tab        next tab on the right side",
		"esc        back to the list from the right side",
		"ctrl+c     quit from the list and the overview",
		"ctrl+] (the default leader key)",
		`leader = "ctrl+o"`,
		"ctrl+] n    create a session",
		"ctrl+] o    create a one-off session",
		"ctrl+] p    open the project switcher",
		"ctrl+] c    confirm clean",
		"ctrl+] r    restart a stopped session",
		"ctrl+] l    edit the session's label",
		"ctrl+] /    filter sessions by name",
		"ctrl+] x    clear the filter",
		"ctrl+] b    show the Build tab",
		"ctrl+] i    open the inbox",
		"ctrl+] P    pause the build",
		"ctrl+] U    resume the build",
		"ctrl+] f    focus the right side",
		"ctrl+] t    switch the right side between agent and overview",
		"ctrl+] \\    close the pane from the list",
		"ctrl+] g    hand the mouse to the terminal",
		"ctrl+] w    resize the panes",
		"ctrl+] ?    show the full key help",
		"ctrl+] q    quit",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("dash usage missing %q:\n%s", want, got)
		}
	}
	// The old keys must not appear as ways to run an action. The bare letters
	// are checked with their column padding so a leader row cannot match them.
	for _, stale := range []string{
		"ctrl+t ",
		"ctrl-\\",
		"ctrl-g",
		"enter/f",
		"n new",
		"c clean",
		"q quit",
		"p open the project switcher",
		"  r          restart",
		"  n          create",
		"  c          confirm",
		"  q          quit",
	} {
		if strings.Contains(got, stale) {
			t.Errorf("dash usage retains old key %q:\n%s", stale, got)
		}
	}
}

// TestModelWritesTheEntryReadEntryScans is THE load-bearing test in this file,
// and it is the one that would have caught the click-to-focus regression.
//
// It is the retargeted form of an assertion that used to run through
// runDashWith's injected-program seam: the write belongs to the ui.Model now
// (the Model is the only thing that knows the project set), so this drives a
// real Model's Init the way Bubble Tea would and reads the result back through
// dashmeta.ReadEntry — the same function `afk notify` uses, so a write landing
// at a path the reader does not scan fails here rather than silently in the
// field.
//
// What it catches: the dash startup path failing to publish
// <git-common-dir>/afk-dash/<pid>.json with usable values. Every component of
// that write was already unit-tested in isolation — dashmeta.WriteEntry
// round-trips in its own package, ResolveTarget resolves a Target from an Entry,
// AlerterArgs emits --actions Focus for a focusable target — and the whole
// feature was still dead in production, because nothing asserted that startup
// CALLS the write.
//
// The recordable rows are load-bearing for the opposite reason: they pin that a
// dashboard outside tmux writes NOTHING. An entry with no pane would give every
// toast a Focus button that cannot act. They also pin where that decision lives
// after the move: internal/ui cannot see dashLocation.recordable, so the four
// values travel unconditionally and dashmeta.WriteEntry's own gates — the ones
// recordable mirrors — are what refuse.
func TestModelWritesTheEntryReadEntryScans(t *testing.T) {
	tests := []struct {
		name      string
		loc       dashLocation
		wantEntry bool
	}{
		{
			name: "inside tmux with an absolute git common dir",
			loc: dashLocation{
				tmux:      "/private/tmp/tmux-501/default,91850,3",
				paneID:    "%68",
				clientTTY: "/dev/ttys000",
				startedAt: "2026-08-05T16:13:21-07:00",
			},
			wantEntry: true,
		},
		{
			// A dashboard in a plain terminal. TMUX unset is the normal case
			// outside a multiplexer, and there is no pane to advertise.
			name: "outside tmux",
			loc: dashLocation{
				paneID:    "",
				tmux:      "",
				startedAt: "2026-08-05T16:13:21-07:00",
			},
			wantEntry: false,
		},
		{
			// TMUX set but TMUX_PANE empty: a pane-less value cannot be switched
			// to, so recording it would produce an unusable target rather than
			// an absent one.
			name: "tmux set but pane empty",
			loc: dashLocation{
				tmux:      "/private/tmp/tmux-501/default,91850,3",
				paneID:    "",
				clientTTY: "/dev/ttys000",
				startedAt: "2026-08-05T16:13:21-07:00",
			},
			wantEntry: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gitCommonDir := t.TempDir()
			loc := tt.loc
			loc.gitCommonDir = gitCommonDir

			m := dashEntryModel(loc)
			dispatchInitBatch(t, m.Init())

			entry, found := waitForEntry(t, gitCommonDir)
			if found != tt.wantEntry {
				t.Fatalf("ReadEntry found=%t while the dash was up, want %t", found, tt.wantEntry)
			}
			if !tt.wantEntry {
				return
			}
			if entry.PaneID != tt.loc.paneID {
				t.Errorf("entry PaneID = %q, want %q", entry.PaneID, tt.loc.paneID)
			}
			if entry.ClientTTY != tt.loc.clientTTY {
				t.Errorf("entry ClientTTY = %q, want %q", entry.ClientTTY, tt.loc.clientTTY)
			}
			if entry.Tmux != tt.loc.tmux {
				t.Errorf("entry Tmux = %q, want %q", entry.Tmux, tt.loc.tmux)
			}
			if entry.StartedAt != tt.loc.startedAt {
				t.Errorf("entry StartedAt = %q, want %q", entry.StartedAt, tt.loc.startedAt)
			}
			if entry.PID != os.Getpid() {
				t.Errorf("entry PID = %d, want this process %d", entry.PID, os.Getpid())
			}

			// The entry must be gone once the dashboard quits, or a click after
			// a clean quit would be aimed at a pane that no longer hosts a
			// dashboard. Removal is synchronous inside requestQuit (a Cmd
			// returned beside tea.Quit may never run), so there is nothing to
			// wait for here.
			quitDash(m)
			if _, still := dashmeta.ReadEntry(gitCommonDir); still {
				t.Errorf("entry still present after the dashboard quit")
			}
		})
	}
}

// TestModelEntryLandsAtTheDocumentedPath pins the LOCATION independently of
// ReadEntry. Both the writer and the reader agreeing on a wrong directory is a
// mutually-consistent failure the round-trip test above cannot see, and the whole
// point of the git common dir is that a hook running in ANY worktree finds it.
//
// It also pins the COUNT at one. Entry ownership sits on the Model now, and the
// belt-and-suspenders removal left behind in runDashWith is the only piece of
// this that still lives in package main — a second writer here would be two
// entries for one dashboard, which ReadEntry would resolve by picking whichever
// it read first.
func TestModelEntryLandsAtTheDocumentedPath(t *testing.T) {
	gitCommonDir := t.TempDir()
	loc := dashLocation{
		gitCommonDir: gitCommonDir,
		tmux:         "/private/tmp/tmux-501/default,91850,3",
		paneID:       "%68",
		clientTTY:    "/dev/ttys000",
		startedAt:    "2026-08-05T16:13:21-07:00",
	}

	m := dashEntryModel(loc)
	dispatchInitBatch(t, m.Init())
	if _, found := waitForEntry(t, gitCommonDir); !found {
		t.Fatal("no entry was written while the dashboard was up")
	}
	defer quitDash(m)

	matches, err := filepath.Glob(filepath.Join(gitCommonDir, "afk-dash", "*.json"))
	if err != nil {
		t.Fatalf("globbing afk-dash/: %v", err)
	}
	want := dashmeta.EntryPath(gitCommonDir)
	if len(matches) != 1 || matches[0] != want {
		t.Errorf("afk-dash/ holds %v, want exactly [%q]", matches, want)
	}
	if !strings.HasSuffix(want, filepath.Join("afk-dash", pidJSON())) {
		t.Errorf("EntryPath = %q, want it to end in afk-dash/<pid>.json", want)
	}
}

// TestRunDashLeavesTheEntryToTheModel is the other half of the move, and it is
// an assertion about absence: runDashWith must no longer write the entry itself.
//
// Two writers is the failure it guards. Both would target the same
// afk-dash/<pid>.json, so nothing would look wrong from the outside — until
// W5.T3 widens the Model's write to one entry per project and the startup path is
// still publishing a single entry for the repo the dashboard happened to launch
// in, which is then the one entry that never gets removed when that project
// leaves the list.
func TestRunDashLeavesTheEntryToTheModel(t *testing.T) {
	gitCommonDir := t.TempDir()
	loc := dashLocation{
		gitCommonDir: gitCommonDir,
		tmux:         "/private/tmp/tmux-501/default,91850,3",
		paneID:       "%68",
		clientTTY:    "/dev/ttys000",
		startedAt:    "2026-08-05T16:13:21-07:00",
	}

	// Asserted from INSIDE the injected program, because that is the only moment
	// a startup-path write would exist: the defer removes it on the way out, so a
	// check after the call would see an empty directory either way.
	var matches []string
	run := func(repo string, interval time.Duration, openSession string, got dashLocation) error {
		var err error
		matches, err = filepath.Glob(filepath.Join(gitCommonDir, "afk-dash", "*.json"))
		return err
	}
	if err := runDashWith("/repo", 2*time.Second, "", loc, run); err != nil {
		t.Fatalf("runDashWith: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("runDashWith wrote %v; the Model owns the entry now", matches)
	}
}

// TestRunDashRemovesTheEntryAfterAnAbnormalExit is why the defer stayed behind.
//
// The Model removes its entry in requestQuit, which only runs on a CLEAN quit. A
// tea.Program that returns an error — a terminal that cannot be put into the alt
// screen, a panic recovered by bubbletea, a failed renderer — never reaches it,
// and the entry left behind is one `afk notify` would then aim a Focus button
// at, for a pane with no dashboard in it. The success criterion is no stale
// entries, so the removal exists on both paths and has to be idempotent: the
// second call below finds nothing to remove and must still be a non-event.
func TestRunDashRemovesTheEntryAfterAnAbnormalExit(t *testing.T) {
	gitCommonDir := t.TempDir()
	loc := dashLocation{
		gitCommonDir: gitCommonDir,
		tmux:         "/private/tmp/tmux-501/default,91850,3",
		paneID:       "%68",
		clientTTY:    "/dev/ttys000",
		startedAt:    "2026-08-05T16:13:21-07:00",
	}

	// Stands in for the Model's own write: the program stub below returns before
	// any Model exists, so the entry has to be planted to have something to leak.
	if err := dashmeta.WriteEntry(loc.gitCommonDir, loc.tmux, loc.paneID, loc.clientTTY, loc.startedAt); err != nil {
		t.Fatalf("WriteEntry: %v", err)
	}
	if _, found := dashmeta.ReadEntry(gitCommonDir); !found {
		t.Fatal("precondition: the planted entry is not readable")
	}

	crash := func(string, time.Duration, string, dashLocation) error { return errSentinel }
	if err := runDashWith("/repo", 2*time.Second, "", loc, crash); err != errSentinel {
		t.Fatalf("runDashWith error = %v, want %v", err, errSentinel)
	}
	if _, found := dashmeta.ReadEntry(gitCommonDir); found {
		t.Error("entry survived an abnormal exit — the defer in runDashWith is what removes it")
	}

	// The idempotency half: a second abnormal exit with nothing left to remove
	// must still propagate the program error rather than reporting the missing
	// file. Both removal paths run in the ordinary case (the Model on quit, then
	// the defer), so one of them always finds the file already gone.
	if err := runDashWith("/repo", 2*time.Second, "", loc, crash); err != errSentinel {
		t.Fatalf("second runDashWith error = %v, want %v", err, errSentinel)
	}
}

// TestRunDashPassesTheLocationToTheDashboard pins the OTHER direction of the
// rendezvous, plus the threading the move depends on. The git common dir carries
// focus requests back from a clicked notification (internal/ui's
// focusrequest.go), and passing "" there disables click-to-focus silently — no
// error, no log, a dashboard that simply never sees a request.
//
// The whole dashLocation travels now rather than the git common dir alone,
// because the Model performs the write and needs the tmux values to put in it. A
// dropped value here is an entry with an empty pane id, which resolves to no
// target at all — the same silence, one step further along.
func TestRunDashPassesTheLocationToTheDashboard(t *testing.T) {
	loc := dashLocation{
		gitCommonDir: t.TempDir(),
		tmux:         "/private/tmp/tmux-501/default,91850,3",
		paneID:       "%68",
		clientTTY:    "/dev/ttys000",
		startedAt:    "2026-08-05T16:13:21-07:00",
	}

	var gotRepo, gotOpen string
	var gotLoc dashLocation
	var gotInterval time.Duration
	run := func(repo string, interval time.Duration, openSession string, l dashLocation) error {
		gotRepo, gotInterval, gotOpen, gotLoc = repo, interval, openSession, l
		return nil
	}
	if err := runDashWith("/some/repo", 5*time.Second, "my-session", loc, run); err != nil {
		t.Fatalf("runDashWith: %v", err)
	}

	if gotLoc != loc {
		t.Errorf("dashboard got location %+v, want %+v", gotLoc, loc)
	}
	if gotRepo != "/some/repo" {
		t.Errorf("dashboard got repo %q, want %q", gotRepo, "/some/repo")
	}
	if gotOpen != "my-session" {
		t.Errorf("dashboard got openSession %q, want %q", gotOpen, "my-session")
	}
	if gotInterval != 5*time.Second {
		t.Errorf("dashboard got interval %v, want %v", gotInterval, 5*time.Second)
	}
}

// TestRunDashPropagatesTheProgramError verifies the dashboard's own failure still
// reaches cmdDash, which exits non-zero on it. The removal wrapper must not
// swallow it while swallowing the dashmeta error around it.
func TestRunDashPropagatesTheProgramError(t *testing.T) {
	wantErr := errSentinel
	err := runDashWith("/repo", time.Second, "", dashLocation{gitCommonDir: t.TempDir()},
		func(string, time.Duration, string, dashLocation) error { return wantErr })
	if err != wantErr {
		t.Errorf("runDashWith error = %v, want %v", err, wantErr)
	}
}

// TestDashDepsThreadsTheDashLocation pins the plain-value half of the seam the
// Model's write depends on: dashDeps is where cmdDash's resolved dashLocation
// becomes ui.Deps, and every one of those five values is silently droppable.
// Nothing in internal/ui can tell a value that was never threaded from a value
// the environment genuinely did not have — both are the empty string — so the
// resulting entry is simply unusable, with no error anywhere.
func TestDashDepsThreadsTheDashLocation(t *testing.T) {
	loc := dashLocation{
		gitCommonDir: "/repo/.git",
		tmux:         "/private/tmp/tmux-501/default,91850,3",
		paneID:       "%68",
		clientTTY:    "/dev/ttys000",
		startedAt:    "2026-08-05T16:13:21-07:00",
	}
	deps := dashDeps("sess", loc, nil, -1)

	for _, tt := range []struct{ field, got, want string }{
		{"GitCommonDir", deps.GitCommonDir, loc.gitCommonDir},
		{"Tmux", deps.Tmux, loc.tmux},
		{"PaneID", deps.PaneID, loc.paneID},
		{"ClientTTY", deps.ClientTTY, loc.clientTTY},
		{"StartedAt", deps.StartedAt, loc.startedAt},
		{"InitialSessionName", deps.InitialSessionName, "sess"},
	} {
		if tt.got != tt.want {
			t.Errorf("dashDeps(...).%s = %q, want %q", tt.field, tt.got, tt.want)
		}
	}

	// Verify the two new seams are wired and non-nil.
	if deps.LoadSelections == nil {
		t.Error("dashDeps(...).LoadSelections = nil, want non-nil")
	}
	if deps.RememberSelection == nil {
		t.Error("dashDeps(...).RememberSelection = nil, want non-nil")
	}
	// Verify the two usage-count seams are wired and non-nil.
	if deps.LoadProjectOpens == nil {
		t.Error("dashDeps(...).LoadProjectOpens = nil, want non-nil")
	}
	if deps.RecordProjectOpen == nil {
		t.Error("dashDeps(...).RecordProjectOpen = nil, want non-nil")
	}
	// Verify the sidebar split seams are wired and non-nil.
	if deps.LoadSidebarPct == nil {
		t.Error("dashDeps(...).LoadSidebarPct = nil, want non-nil")
	}
	if deps.RememberSidebarPct == nil {
		t.Error("dashDeps(...).RememberSidebarPct = nil, want non-nil")
	}
}

// TestDashLocationRecordable is the gate matrix, and it must agree with
// dashmeta.WriteEntry's own three gates row for row: the AFK_DEBUG line reports
// recordable(), so a disagreement would print "recordable=true" about a write that
// never happened and send the next debugger looking in the wrong place entirely.
//
// The relative-path row is load-bearing: `git rev-parse --git-common-dir` WITHOUT
// --path-format=absolute returns a relative path, every dashmeta writer no-ops on
// one, and the resulting silence is indistinguishable from the regression this
// file exists for.
func TestDashLocationRecordable(t *testing.T) {
	tests := []struct {
		name string
		loc  dashLocation
		want bool
	}{
		{
			name: "all three present",
			loc:  dashLocation{gitCommonDir: "/repo/.git", tmux: "/tmp/tmux,1,2", paneID: "%1"},
			want: true,
		},
		{
			name: "relative git common dir",
			loc:  dashLocation{gitCommonDir: ".git", tmux: "/tmp/tmux,1,2", paneID: "%1"},
			want: false,
		},
		{
			name: "empty git common dir",
			loc:  dashLocation{gitCommonDir: "", tmux: "/tmp/tmux,1,2", paneID: "%1"},
			want: false,
		},
		{
			name: "no tmux",
			loc:  dashLocation{gitCommonDir: "/repo/.git", tmux: "", paneID: "%1"},
			want: false,
		},
		{
			name: "no pane",
			loc:  dashLocation{gitCommonDir: "/repo/.git", tmux: "/tmp/tmux,1,2", paneID: ""},
			want: false,
		},
		{
			name: "zero value fails closed",
			loc:  dashLocation{},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.loc.recordable(); got != tt.want {
				t.Errorf("recordable() = %t, want %t", got, tt.want)
			}
			// The agreement check: WriteEntry against a real directory must write
			// a file exactly when recordable() says it will.
			if !filepath.IsAbs(tt.loc.gitCommonDir) {
				return
			}
			dir := t.TempDir()
			loc := tt.loc
			loc.gitCommonDir = dir
			if err := dashmeta.WriteEntry(dir, loc.tmux, loc.paneID, loc.clientTTY, "2026-08-05T12:00:00Z"); err != nil {
				t.Fatalf("WriteEntry: %v", err)
			}
			_, found := dashmeta.ReadEntry(dir)
			if found != loc.recordable() {
				t.Errorf("WriteEntry wrote=%t but recordable()=%t — the gates disagree",
					found, loc.recordable())
			}
		})
	}
}

// TestDashLocationDiagnostic pins the AFK_DEBUG line's content. The line is the
// designated tool for diagnosing an empty afk-dash/, so the two facts a debugger
// cannot get any other way have to be in it: whether the gates passed, and the
// absolute path the entry would land at.
func TestDashLocationDiagnostic(t *testing.T) {
	loc := dashLocation{
		gitCommonDir: "/Users/x/ontra/afk/.git",
		tmux:         "/private/tmp/tmux-501/default,91850,3",
		paneID:       "%68",
		clientTTY:    "/dev/ttys000",
		startedAt:    "2026-08-05T16:13:21-07:00",
	}
	got := dashLocationDiagnostic(loc)
	for _, want := range []string{
		"/Users/x/ontra/afk/.git",
		"%68",
		"/dev/ttys000",
		"recordable=true",
		dashmeta.EntryPath(loc.gitCommonDir),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("diagnostic %q missing %q", got, want)
		}
	}

	// An unrecordable location must say so AND not print a path that does not
	// exist: "(none)" is the honest answer, and a plausible-looking path here
	// would send a debugger hunting for a file nothing ever wrote.
	got = dashLocationDiagnostic(dashLocation{})
	if !strings.Contains(got, "recordable=false") {
		t.Errorf("zero-value diagnostic %q should report recordable=false", got)
	}
	if !strings.Contains(got, "entry=(none)") {
		t.Errorf("zero-value diagnostic %q should report entry=(none)", got)
	}
}

// TestResolveGitCommonDirInUsesItsDirArgument is the regression guard for the
// ambient-cwd dependency. resolveGitCommonDirIn must answer for the directory it
// is GIVEN, not the one the process happens to be in, because `afk new`'s
// handoff re-execs `afk dash --open` as a child and the value crosses that
// boundary.
//
// Load-bearing detail: the answer must be an ABSOLUTE path. The bare
// --git-common-dir form returns a relative one, dashmeta silently no-ops on it,
// and click-to-focus dies with no error anywhere — so this asserts the property
// the --path-format=absolute flag exists to provide, rather than the flag itself.
func TestResolveGitCommonDirInUsesItsDirArgument(t *testing.T) {
	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}

	got := resolveGitCommonDirIn(repoRoot)
	if got == "" {
		t.Fatalf("resolveGitCommonDirIn(%q) = \"\", want this repo's git common dir", repoRoot)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("resolveGitCommonDirIn(%q) = %q, want an absolute path", repoRoot, got)
	}

	// A directory outside any git repo must answer "", never inherit this
	// process's repo. t.TempDir is under /var/folders, which is not a repo.
	if outside := resolveGitCommonDirIn(t.TempDir()); outside != "" {
		t.Errorf("resolveGitCommonDirIn(non-repo) = %q, want \"\"", outside)
	}

	// Asserted from the MAIN worktree specifically, because that is the only
	// place the absolute-path property can actually fail. From a LINKED worktree
	// (which is where these tests normally run) git answers with an absolute path
	// even without --path-format=absolute, so dropping the flag is invisible
	// there — verified by mutation: removing it left this test green until this
	// row existed. From the main worktree the bare form returns ".git", which
	// dashmeta silently refuses, and click-to-focus dies with no error anywhere.
	if main := filepath.Dir(got); filepath.Base(got) == ".git" && isDir(main) {
		if fromMain := resolveGitCommonDirIn(main); !filepath.IsAbs(fromMain) {
			t.Errorf("resolveGitCommonDirIn(%q) = %q, want an absolute path — "+
				"--path-format=absolute is missing", main, fromMain)
		}
	}

	// And the same call with no explicit dir must agree, since the test process's
	// cwd IS the repo root — this is what pins resolveGitCommonDir as a thin
	// wrapper rather than a second implementation.
	if bare := resolveGitCommonDir(); bare != got {
		t.Errorf("resolveGitCommonDir() = %q, resolveGitCommonDirIn(cwd) = %q; want agreement", bare, got)
	}
}

// quitImmediately is a Model that ends the program from Init, so a real
// tea.Program can be run headless in a test: the view it is given is applied and
// the resulting escape sequences written before it exits.
//
// It takes the tea.View to return rather than building one, because under Bubble
// Tea v2 the terminal modes a program enters are carried BY the view. A fixture
// that hard-coded its own would prove only that the fixture is right.
type quitImmediately struct{ view tea.View }

func (q quitImmediately) Init() tea.Cmd                       { return tea.Quit }
func (q quitImmediately) Update(tea.Msg) (tea.Model, tea.Cmd) { return q, tea.Quit }
func (q quitImmediately) View() tea.View                      { return q.view }

// TestDashProgramOptionsAskTheTerminalForMouseReports closes the one gap every
// other mouse test in this repo shares: they inject tea.MouseMsg directly into
// Update, which works whether or not the program ever asked the terminal to
// report mouse events. Take the request away and the entire suite stays green
// while the feature is completely inert — no wheel notch ever reaches the
// dashboard, because the terminal was never told to send one.
//
// Under Bubble Tea v1 the request was tea.WithMouseCellMotion(), a
// tea.ProgramOption, and this test ran the real constructor to pin that the
// option list and the call that applies it agreed. v2 deleted both that option
// and tea.WithAltScreen(): the alternate screen and the mouse mode are now
// fields on the tea.View a Model returns. So the same guarantee is asserted in
// two halves, and BOTH are load-bearing:
//
//  1. the real dashboard Model DECLARES the two modes — the half that fails if
//     someone drops AltScreen or MouseMode from ui.Model.View; and
//  2. those declarations are what actually put ?1002/?1006/?1049 on the wire —
//     the half that fails if a future bubbletea stops honouring them, which is
//     unobservable from the field values alone.
//
// Half 2 runs the program over the view half 1 just read off the real Model,
// not over a hand-written one, so no fixture sits between the assertion and the
// dashboard. The bytes are still the subject: ?1002 is cell-motion reporting and
// ?1006 is the SGR encoding the coordinate translation in internal/ui/mouse.go
// decodes.
func TestDashProgramOptionsAskTheTerminalForMouseReports(t *testing.T) {
	dashView := dashEntryModel(dashLocation{}).View()

	if !dashView.AltScreen {
		t.Errorf("the dashboard's View does not ask for the alternate screen; it would paint over the user's scrollback")
	}
	if dashView.MouseMode != tea.MouseModeCellMotion {
		t.Errorf("the dashboard's View declares MouseMode %v, want tea.MouseModeCellMotion (%v) — nothing else reports wheel notches and drags",
			dashView.MouseMode, tea.MouseModeCellMotion)
	}

	var out bytes.Buffer
	p := newDashProgram(quitImmediately{view: dashView},
		tea.WithInput(strings.NewReader("")),
		tea.WithOutput(&out),
		tea.WithoutSignalHandler(),
	)
	if _, err := p.Run(); err != nil {
		t.Fatalf("run: %v", err)
	}

	for _, want := range []struct{ seq, why string }{
		{"\x1b[?1002h", "cell-motion mouse reporting (View.MouseMode = tea.MouseModeCellMotion)"},
		{"\x1b[?1006h", "SGR mouse encoding (implied by MouseModeCellMotion)"},
		{"\x1b[?1049h", "the alternate screen (View.AltScreen)"},
	} {
		if !strings.Contains(out.String(), want.seq) {
			t.Errorf("the dashboard never enabled %s — %q is absent from what it wrote (%q)",
				want.why, want.seq, out.String())
		}
	}
}

// TestPickActiveProject is the decision matrix for W2.T2's --project/dash
// wiring. The synthetic rows pin the pure string-matching contract (both
// sides are assumed already normalized — see the two real-repo rows below for
// why that assumption is safe); the "no cwd repo, empty list" row is the
// load-bearing one for launching outside any repo, since fabricating an entry
// there would defeat W5.T1's empty state before it exists.
func TestPickActiveProject(t *testing.T) {
	tests := []struct {
		name         string
		projects     []project.Project
		cwdRepo      string
		wantLen      int
		wantIdx      int
		wantAppended bool // true: the entry at wantIdx must be the ad-hoc pinned one
	}{
		{
			name:     "cwd repo present in list",
			projects: []project.Project{{Path: "/a", Name: "a"}, {Path: "/b", Name: "b"}},
			cwdRepo:  "/b",
			wantLen:  2,
			wantIdx:  1,
		},
		{
			name:     "cwd repo already pinned stays matched, not re-appended",
			projects: []project.Project{{Path: "/a", Name: "a", Pinned: true}},
			cwdRepo:  "/a",
			wantLen:  1,
			wantIdx:  0,
		},
		{
			name:         "cwd repo absent from a non-empty list is appended pinned and selected",
			projects:     []project.Project{{Path: "/a", Name: "a"}},
			cwdRepo:      "/c",
			wantLen:      2,
			wantIdx:      1,
			wantAppended: true,
		},
		{
			// The ordinary `afk dash` startup case: no scan has run yet,
			// so the candidate list is empty and the only entry is the ad-hoc
			// cwd one (D6).
			name:         "cwd repo absent from an empty list is appended pinned and selected",
			projects:     nil,
			cwdRepo:      "/c",
			wantLen:      1,
			wantIdx:      0,
			wantAppended: true,
		},
		{
			name:     "no cwd repo, non-empty list selects the first entry (D7)",
			projects: []project.Project{{Path: "/a", Name: "a"}, {Path: "/b", Name: "b"}},
			cwdRepo:  "",
			wantLen:  2,
			wantIdx:  0,
		},
		{
			// Must not be fabricated: -1 plus the unchanged, empty list is
			// what lets the caller pass it through to the dashboard's empty
			// state instead of inventing a project.
			name:     "no cwd repo, empty list stays empty at -1",
			projects: nil,
			cwdRepo:  "",
			wantLen:  0,
			wantIdx:  -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			updated, idx := pickActiveProject(tt.projects, tt.cwdRepo)
			if len(updated) != tt.wantLen {
				t.Fatalf("pickActiveProject(...) len = %d, want %d (result: %+v)", len(updated), tt.wantLen, updated)
			}
			if idx != tt.wantIdx {
				t.Errorf("pickActiveProject(...) index = %d, want %d", idx, tt.wantIdx)
			}
			if tt.wantAppended {
				if idx < 0 || idx >= len(updated) {
					t.Fatalf("index %d out of range for result %+v", idx, updated)
				}
				got := updated[idx]
				if got.Path != tt.cwdRepo {
					t.Errorf("appended entry Path = %q, want %q", got.Path, tt.cwdRepo)
				}
				if !got.Pinned {
					t.Errorf("appended entry Pinned = false, want true (D6)")
				}
			}
		})
	}

	t.Run("appending a colliding basename re-labels both entries", func(t *testing.T) {
		updated, idx := pickActiveProject([]project.Project{{Path: "/x/api", Name: "api"}}, "/y/api")
		if len(updated) != 2 {
			t.Fatalf("len(updated) = %d, want 2", len(updated))
		}
		if updated[0].Name != "x/api" || updated[idx].Name != "y/api" {
			t.Errorf("names = %q, %q, want %q, %q", updated[0].Name, updated[idx].Name, "x/api", "y/api")
		}
	})

	// The remaining two rows exercise the REAL normalization pipeline
	// (resolveCwdRepoRoot) rather than synthetic already-normalized strings:
	// they are what proves a subdirectory launch and a symlinked path both
	// converge on the same string pickActiveProject compares against, so
	// neither produces a duplicate row. Path identity here is a plain string
	// comparison by design, so this is the only place either case can be
	// demonstrated end to end.
	t.Run("cwd inside a subdirectory of a listed repo matches its root, not a second row", func(t *testing.T) {
		repo := newTestRepo(t)
		sub := filepath.Join(repo, "sub", "dir")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
		projects := []project.Project{{Path: repo, Name: filepath.Base(repo)}}

		cwdRepo := resolveCwdRepoRoot(sub)
		if cwdRepo != repo {
			t.Fatalf("resolveCwdRepoRoot(subdirectory) = %q, want the repo root %q", cwdRepo, repo)
		}

		updated, idx := pickActiveProject(projects, cwdRepo)
		if len(updated) != 1 {
			t.Errorf("len(updated) = %d, want 1 (one row, not two)", len(updated))
		}
		if idx != 0 {
			t.Errorf("idx = %d, want 0 (matched the existing entry)", idx)
		}
	})

	t.Run("a cwd path differing from a listed one only by a symlink dedupes to one entry", func(t *testing.T) {
		repo := newTestRepo(t) // already symlink-resolved, per its own doc comment
		link := filepath.Join(t.TempDir(), "link-to-repo")
		if err := os.Symlink(repo, link); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		projects := []project.Project{{Path: repo, Name: filepath.Base(repo)}}

		cwdRepo := resolveCwdRepoRoot(link)
		if cwdRepo != repo {
			t.Fatalf("resolveCwdRepoRoot(symlink) = %q, want the resolved repo root %q", cwdRepo, repo)
		}

		updated, idx := pickActiveProject(projects, cwdRepo)
		if len(updated) != 1 {
			t.Errorf("len(updated) = %d, want 1 (deduped to one entry)", len(updated))
		}
		if idx != 0 {
			t.Errorf("idx = %d, want 0 (matched the existing entry)", idx)
		}
	})
}

// TestResolveCwdRepoRoot pins the impure normalization pickActiveProject
// depends on: a repo root, a subdirectory of one, and a path outside any repo
// must each answer correctly, because pickActiveProject only does a plain
// string comparison and trusts this function to have already resolved
// everything to the same canonical form scan.Scan uses.
func TestResolveCwdRepoRoot(t *testing.T) {
	repo := newTestRepo(t)

	if got := resolveCwdRepoRoot(repo); got != repo {
		t.Errorf("resolveCwdRepoRoot(repo root) = %q, want %q", got, repo)
	}

	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", sub, err)
	}
	if got := resolveCwdRepoRoot(sub); got != repo {
		t.Errorf("resolveCwdRepoRoot(subdirectory) = %q, want the repo root %q", got, repo)
	}

	// A directory outside any git repo must answer "", never inherit this
	// test process's own repo the way resolveGitCommonDirIn's analogous test
	// pins for that function.
	outside := t.TempDir()
	if got := resolveCwdRepoRoot(outside); got != "" {
		t.Errorf("resolveCwdRepoRoot(non-repo) = %q, want \"\"", got)
	}
}

// TestDashInitialProjects covers the two cases `afk dash` actually hits on
// startup — dashInitialProjects never has a pre-scanned list to consult, so
// TestPickActiveProject's fuller matrix is what proves the general decision
// logic. Starting inside a repo pins a single Pinned entry for it and selects
// it; starting outside any repo — no search-root scan has run yet, per the
// plan's "never delay the first frame" constraint — leaves the list empty at
// index -1 for W5.T1's empty state to render.
func TestDashInitialProjects(t *testing.T) {
	t.Run("inside a repo", func(t *testing.T) {
		repo := newTestRepo(t)

		projects, active := dashInitialProjects(repo)
		if len(projects) != 1 {
			t.Fatalf("len(projects) = %d, want 1", len(projects))
		}
		if projects[0].Path != repo {
			t.Errorf("projects[0].Path = %q, want %q", projects[0].Path, repo)
		}
		if !projects[0].Pinned {
			t.Errorf("projects[0].Pinned = false, want true (D6: no search root produces this entry)")
		}
		if active != 0 {
			t.Errorf("active = %d, want 0", active)
		}
	})

	t.Run("outside any repo", func(t *testing.T) {
		outside := t.TempDir()

		projects, active := dashInitialProjects(outside)
		if len(projects) != 0 {
			t.Errorf("len(projects) = %d, want 0 (nothing fabricated)", len(projects))
		}
		if active != -1 {
			t.Errorf("active = %d, want -1", active)
		}
	})
}

// TestDashDepsWiresScanAndLoadConfig is the regression test for the exact
// failure this task exists to prevent: W3.T1's ui.Deps.Scan/LoadConfig seams
// are nil-inert by default (a deliberate test-hermeticity gate — see
// ui.Deps.withDefaults), so a dashboard built without this wiring runs with a
// dead scan pipeline and no code path ever notices, because every other test
// in this suite is happy with an inert scan. Asserting on dashDeps's return
// value — the same call runDashProgram makes — is what makes "on the path
// cmdDash actually takes" true rather than aspirational.
func TestDashDepsWiresScanAndLoadConfig(t *testing.T) {
	deps := dashDeps("", dashLocation{}, nil, -1)
	if deps.Scan == nil {
		t.Error("dashDeps(...).Scan is nil, want the scanProjects wrapper")
	}
	if deps.LoadConfig == nil {
		t.Error("dashDeps(...).LoadConfig is nil, want the loadDashConfig wrapper")
	}
	if deps.LoadListed == nil {
		t.Error("dashDeps(...).LoadListed is nil, want the dashLoadListed wrapper")
	}
}

// TestScanProjectsWalksRealSearchRoot exercises scanProjects — the real
// scan.Scan wrapper handed to ui.Deps.Scan — against a real repo on disk, not
// a fake Deps.Run. maxDepth=1 (rather than 0, scan.Scan's own default) pins
// that the argument actually reaches scan.Deps.MaxDepth: it is a parameter
// specifically because dashDeps runs before any config is loaded (see
// scanProjects's doc comment), so a regression that silently dropped it would
// still pass a test that only tried the default. root is resolved through
// filepath.EvalSymlinks before comparison because darwin's TMPDIR is itself a
// symlink (see internal/discover/discover_test.go for the same normalization),
// and scan.Scan returns symlink-resolved paths.
func TestScanProjectsWalksRealSearchRoot(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolving temp dir: %v", err)
	}
	repo := newTestRepo(t)
	if err := os.MkdirAll(filepath.Join(repo, ".afk"), 0o755); err != nil {
		t.Fatalf("seed .afk: %v", err)
	}
	moved := filepath.Join(root, filepath.Base(repo))
	if err := os.Rename(repo, moved); err != nil {
		t.Fatalf("move repo under root: %v", err)
	}

	got := scanProjects([]string{root}, 1)
	if len(got) != 1 || got[0].Path != moved {
		t.Fatalf("scanProjects(%q, 1) = %+v, want exactly [%q]", root, got, moved)
	}
}

// writeDashConfig writes body as config.toml in a fresh config folder, points
// XDG_CONFIG_HOME at it for this test, and returns the file's path. The folder
// is t.TempDir, an absolute path: the settings reader ignores a relative
// XDG_CONFIG_HOME, and a test relying on one would read the developer's own
// file.
func writeDashConfig(t *testing.T, body string) string {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	confDir := filepath.Join(xdg, "afk")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	confPath := filepath.Join(confDir, "config.toml")
	if err := os.WriteFile(confPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return confPath
}

// TestLoadDashConfigReadsRealConfigFile exercises loadDashConfig — the real
// daemonconfig.Load/daemonconfig.Path wrapper handed to ui.Deps.LoadConfig —
// against a real file, redirecting XDG_CONFIG_HOME rather than faking
// daemonconfig.Deps so the assertion is against afk dash's actual wiring, not
// a stand-in for it. Every row is load-bearing:
//
//   - "existing file" pins that a real search root round-trips through the
//     seam scanCmd will call next, and that all three [dash] keys are carried
//     across the conversion (a dropped field would leave the walk at depth 0
//     or the ticker on its fallback, and nothing else would notice).
//   - "missing file" pins the reader's documented contract (no file is not an
//     error, and the defaults come back) all the way through this wrapper,
//     since a caller that turned that into an error would blank the dashboard
//     on the common case of no config at all.
//   - "a bad [dash] value" is the row this wrapper exists for. The reader
//     never fails its load over a [dash] value (the daemon shares that load),
//     so it is this wrapper alone that must hand the carried error to the
//     dashboard, with the path. Without the row, a wrapper that ignored
//     DashErr would search with the defaults and never say the owner's
//     setting was refused. The row also checks the reader's own load of the
//     same file is still nil, which is the other half of the rule.
func TestLoadDashConfigReadsRealConfigFile(t *testing.T) {
	t.Run("existing file", func(t *testing.T) {
		root := t.TempDir()
		confPath := writeDashConfig(t, fmt.Sprintf("[dash]\nsearch_roots = [%q]\nscan_interval = \"30s\"\nmax_depth = 2\n", root))

		cfg, path, err := loadDashConfig()
		if err != nil {
			t.Fatalf("loadDashConfig() error = %v", err)
		}
		if path != confPath {
			t.Errorf("path = %q, want %q", path, confPath)
		}
		if len(cfg.Roots) != 1 || cfg.Roots[0] != root {
			t.Errorf("Roots = %v, want [%q]", cfg.Roots, root)
		}
		if cfg.Interval != 30*time.Second {
			t.Errorf("Interval = %v, want 30s", cfg.Interval)
		}
		if cfg.MaxDepth != 2 {
			t.Errorf("MaxDepth = %d, want 2", cfg.MaxDepth)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		xdg := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", xdg)

		cfg, path, err := loadDashConfig()
		if err != nil {
			t.Fatalf("loadDashConfig() error = %v, want nil (missing file is the ordinary case)", err)
		}
		if want := filepath.Join(xdg, "afk", "config.toml"); path != want {
			t.Errorf("path = %q, want the resolved (non-existent) config path %q", path, want)
		}
		if cfg.Roots != nil {
			t.Errorf("Roots = %v, want nil (default config)", cfg.Roots)
		}
		if cfg.Interval != daemonconfig.DefaultScanInterval || cfg.MaxDepth != daemonconfig.DefaultMaxDepth {
			t.Errorf("Interval, MaxDepth = %v, %d, want the defaults %v, %d",
				cfg.Interval, cfg.MaxDepth, daemonconfig.DefaultScanInterval, daemonconfig.DefaultMaxDepth)
		}
	})

	t.Run("a bad [dash] value", func(t *testing.T) {
		root := t.TempDir()
		confPath := writeDashConfig(t, fmt.Sprintf("[dash]\nsearch_roots = [%q]\nscan_interval = \"1ms\"\n", root))

		cfg, path, err := loadDashConfig()
		if err == nil {
			t.Fatal("loadDashConfig() error = nil, want the refused scan_interval reported")
		}
		// Logged so a -v run shows the text the dashboard's toast will carry.
		t.Logf("path = %s", path)
		t.Logf("error = %v", err)
		if !strings.Contains(err.Error(), "dash.scan_interval") || !strings.Contains(err.Error(), confPath) {
			t.Errorf("error = %q, want it to name dash.scan_interval and %s", err, confPath)
		}
		if path != confPath {
			t.Errorf("path = %q, want %q (returned with the error, so the empty state can name the file)", path, confPath)
		}
		if len(cfg.Roots) != 0 {
			t.Errorf("Roots = %v, want none: a refused table must not start a search with part of it", cfg.Roots)
		}
		if _, lerr := daemonconfig.Load(daemonconfig.DefaultDeps()); lerr != nil {
			t.Errorf("daemonconfig.Load error = %v, want nil: only the dashboard's search reports a [dash] value", lerr)
		}
	})
}

// TestScanPipelineEndToEndWithRealConfig chains loadDashConfig into
// scanProjects exactly as messages.go's scanCmd does, and is this task's
// verification of the acceptance criterion that a scan message ends up
// populated "from a real config and a real search root" — checked at the
// ui.Deps seam rather than through the project switcher overlay, which W4.T1
// is delivering concurrently and which this task must not depend on. root is
// resolved through filepath.EvalSymlinks up front (same reason as
// TestScanProjectsWalksRealSearchRoot) since it feeds both the written config
// file and the expected result, and the two must agree on the same form.
func TestScanPipelineEndToEndWithRealConfig(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolving temp dir: %v", err)
	}
	repo := newTestRepo(t)
	if err := os.MkdirAll(filepath.Join(repo, ".afk"), 0o755); err != nil {
		t.Fatalf("seed .afk: %v", err)
	}
	moved := filepath.Join(root, filepath.Base(repo))
	if err := os.Rename(repo, moved); err != nil {
		t.Fatalf("move repo under root: %v", err)
	}
	writeDashConfig(t, fmt.Sprintf("[dash]\nsearch_roots = [%q]\n", root))

	deps := dashDeps("", dashLocation{}, nil, -1)
	cfg, _, err := deps.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	got := deps.Scan(cfg.Roots, cfg.MaxDepth)
	if len(got) != 1 || got[0].Path != moved {
		t.Fatalf("Scan(%v, %d) = %+v, want exactly [%q]", cfg.Roots, cfg.MaxDepth, got, moved)
	}
}

// TestLoadDashConfigNamesThePathOnMalformedConfig pins the contract cmdDash's
// launch path now leans on: the settings load failing to parse must still come
// back WITH the resolved config path. cmdDash deliberately no longer exits 1 on a
// malformed file at launch — the empty state is where the user is told which
// file to fix, and it can only name the file if this wrapper returns the path
// on exactly the error path. A wrapper that returned ("", err) here would turn
// the launch-time guidance back into the generic intro with no path at all.
func TestLoadDashConfigNamesThePathOnMalformedConfig(t *testing.T) {
	confPath := writeDashConfig(t, "[dash\nsearch_roots = \n")

	_, path, err := loadDashConfig()
	if err == nil {
		t.Fatal("loadDashConfig() error = nil, want a parse error for malformed TOML")
	}
	if path != confPath {
		t.Errorf("path = %q, want %q (the empty state needs it on exactly this error path)", path, confPath)
	}
}

// TestLoadSelections tests the production implementation of
// ui.Deps.LoadSelections through fake uistate.Deps. It verifies that errors
// (missing file, malformed JSON) are swallowed and an empty non-nil map is
// returned, so a corrupt or unreadable state file never stops the dashboard
// from starting. The valid-file case returns the entries as expected.
func TestLoadSelections(t *testing.T) {
	tests := []struct {
		name        string
		readFileErr error
		readFileOut []byte
		want        map[string]string
	}{
		{
			name: "missing file",
			// uistate.Load treats ErrNotExist as empty state, which it then
			// wraps in a non-nil map. loadSelectionsWith passes that through.
			readFileErr: os.ErrNotExist,
			want:        map[string]string{},
		},
		{
			name:        "malformed JSON",
			readFileOut: []byte("{not json"),
			// uistate.Load returns an error for malformed JSON, which
			// loadSelectionsWith swallows and returns empty non-nil map.
			want: map[string]string{},
		},
		{
			name:        "valid state file",
			readFileOut: []byte(`{"selected":{"a":"session1","b":"session2"}}`),
			want:        map[string]string{"a": "session1", "b": "session2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := loadSelectionsWith(readOnlySelectionDeps(tt.readFileOut, tt.readFileErr))
			if !maps.Equal(got, tt.want) {
				t.Errorf("loadSelectionsWith(...) = %v, want %v", got, tt.want)
			}
			// Verify the map is non-nil (not just empty) even on errors.
			if got == nil {
				t.Errorf("loadSelectionsWith(...) returned nil map, want non-nil")
			}
		})
	}
}

// readOnlySelectionDeps builds uistate.Deps with every environment-touching
// seam faked, so loadSelectionsWith's error policy is exercised without the
// developer's real $XDG_STATE_HOME, home directory, or state file taking part
// — uistate.Path consults Getenv and HomeDir before anything is read, so
// faking only ReadFile would leave the resolved path environment-dependent.
// The four write seams return an error they are never expected to reach:
// loadSelectionsWith only reads, and a regression that made it write would
// surface as that error rather than as a file on the developer's disk.
func readOnlySelectionDeps(out []byte, readErr error) uistate.Deps {
	unexpected := errors.New("loadSelectionsWith must not write")
	return uistate.Deps{
		Getenv: func(key string) string {
			if key == "XDG_STATE_HOME" {
				return "/fake/state"
			}
			return ""
		},
		HomeDir:   func() (string, error) { return "/fake/home", nil },
		ReadFile:  func(string) ([]byte, error) { return out, readErr },
		WriteFile: func(string, []byte, os.FileMode) error { return unexpected },
		Rename:    func(string, string) error { return unexpected },
		MkdirAll:  func(string, os.FileMode) error { return unexpected },
		Remove:    func(string) error { return unexpected },
	}
}

// TestRememberSelectionSerializesConcurrentSelections covers the production
// writer itself — rememberSelection, the closure dashDeps hands to
// ui.Deps.RememberSelection — rather than uistate.Remember underneath it,
// because the thing this function adds is selectionWriteMu and a test that
// went through uistate directly would not touch it.
//
// The concurrent subtest is the load-bearing one: it reproduces the exact
// shape of the collision selectionWriteMu exists for — the dashboard's tick
// flush on a Cmd goroutine and its quit flush on the UI goroutine, writing
// DISJOINT keys through the same read-modify-write-rename. Every entry must
// survive. It writes through a real temp XDG_STATE_HOME because that is the
// only way to reach rememberSelection, which takes no Deps by design (see
// ui.Deps.RememberSelection's signature). Removing the lock does not make this
// fail on every run — a lost update needs the two writers to interleave — so
// the subtest repeats the concurrent write with a FRESH set of keys each
// round: reusing the same 8 keys across rounds would let a lost update in
// round r be silently rewritten by round r+1, since only the final
// loadSelections() is checked, and repetition would hide the failure mode
// it's meant to expose rather than pin it. Distinct keys per round mean every
// one of the 200 concurrent writes across all rounds must survive
// independently, which is what makes many rounds actually more likely to
// catch a lost update, not just more likely to look like it did.
func TestRememberSelectionSerializesConcurrentSelections(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	t.Run("sequential writes accumulate", func(t *testing.T) {
		if err := rememberSelection("/repo/a", "session-a"); err != nil {
			t.Fatalf("rememberSelection(/repo/a) error = %v", err)
		}
		if err := rememberSelection("/repo/b", "session-b"); err != nil {
			t.Fatalf("rememberSelection(/repo/b) error = %v", err)
		}
		got := loadSelections()
		want := map[string]string{"/repo/a": "session-a", "/repo/b": "session-b"}
		if !maps.Equal(got, want) {
			t.Errorf("loadSelections() = %v, want %v", got, want)
		}
	})

	t.Run("concurrent writes of disjoint keys all survive", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())

		const writers = 8
		const rounds = 25
		want := map[string]string{}
		for round := range rounds {
			var wg sync.WaitGroup
			errs := make([]error, writers)
			for i := range writers {
				wg.Add(1)
				key := fmt.Sprintf("/repo/%d/%d", round, i)
				name := fmt.Sprintf("session-%d-%d", round, i)
				want[key] = name
				go func() {
					defer wg.Done()
					errs[i] = rememberSelection(key, name)
				}()
			}
			wg.Wait()
			for i, err := range errs {
				if err != nil {
					t.Fatalf("round %d: rememberSelection(/repo/%d/%d) error = %v", round, round, i, err)
				}
			}
		}
		got := loadSelections()
		if !maps.Equal(got, want) {
			t.Errorf("after %d rounds of %d concurrent writers with distinct keys, loadSelections() = %v, want %v",
				rounds, writers, got, want)
		}
	})
}

// TestLoadProjectOpens tests the production implementation of
// ui.Deps.LoadProjectOpens through fake uistate.Deps, on exactly
// TestLoadSelections's shape: a missing or malformed state file must degrade
// to an empty non-nil counts map rather than stopping the dashboard from
// starting, and a valid file's opened counts pass through unchanged.
func TestLoadProjectOpens(t *testing.T) {
	tests := []struct {
		name        string
		readFileErr error
		readFileOut []byte
		want        map[string]int
	}{
		{
			name:        "missing file",
			readFileErr: os.ErrNotExist,
			want:        map[string]int{},
		},
		{
			name:        "malformed JSON",
			readFileOut: []byte("{not json"),
			want:        map[string]int{},
		},
		{
			name:        "valid state file",
			readFileOut: []byte(`{"opened":{"a":3,"b":1}}`),
			want:        map[string]int{"a": 3, "b": 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := loadProjectOpensWith(readOnlySelectionDeps(tt.readFileOut, tt.readFileErr))
			if !maps.Equal(got, tt.want) {
				t.Errorf("loadProjectOpensWith(...) = %v, want %v", got, tt.want)
			}
			if got == nil {
				t.Errorf("loadProjectOpensWith(...) returned nil map, want non-nil")
			}
		})
	}
}

// TestLoadSidebarPct tests the production implementation of
// ui.Deps.LoadSidebarPct through fake uistate.Deps, on TestLoadSelections's
// shape: a missing, malformed, or field-less state file degrades to 0 (the
// UI's "use the default split" value) rather than stopping the dashboard from
// starting, and a stored percentage passes through unchanged.
func TestLoadSidebarPct(t *testing.T) {
	tests := []struct {
		name        string
		readFileErr error
		readFileOut []byte
		want        int
	}{
		{
			name:        "missing file",
			readFileErr: os.ErrNotExist,
			want:        0,
		},
		{
			name:        "malformed JSON",
			readFileOut: []byte("{not json"),
			want:        0,
		},
		{
			name:        "valid file without the field",
			readFileOut: []byte(`{"selected":{"a":"session1"}}`),
			want:        0,
		},
		{
			name:        "valid state file",
			readFileOut: []byte(`{"selected":{"a":"session1"},"sidebar_pct":52}`),
			want:        52,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := loadSidebarPctWith(readOnlySelectionDeps(tt.readFileOut, tt.readFileErr))
			if got != tt.want {
				t.Errorf("loadSidebarPctWith(...) = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestRememberSidebarPctRoundTripsAndSharesTheWriteLock covers
// rememberSidebarPct — the closure dashDeps hands to
// ui.Deps.RememberSidebarPct — through the real state file in a temp
// XDG_STATE_HOME. Interleaving it with the other two write seams proves it
// takes the same selectionWriteMu: without the lock, one writer's rename
// would discard another's merged field and a selection or open count would
// go missing.
func TestRememberSidebarPctRoundTripsAndSharesTheWriteLock(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	if err := rememberSidebarPct(50); err != nil {
		t.Fatalf("rememberSidebarPct(50) error = %v", err)
	}
	if got := loadSidebarPct(); got != 50 {
		t.Errorf("loadSidebarPct() = %d, want 50", got)
	}

	const writers = 8
	const rounds = 10
	var wg sync.WaitGroup
	errs := make([]error, rounds*writers*3)
	slot := 0
	for round := range rounds {
		for i := range writers {
			key := fmt.Sprintf("/repo/%d/%d", round, i)
			name := fmt.Sprintf("session-%d-%d", round, i)
			pct := 20 + round
			for _, write := range []func() error{
				func() error { return rememberSelection(key, name) },
				func() error { return recordProjectOpen("/repo/shared") },
				func() error { return rememberSidebarPct(pct) },
			} {
				wg.Add(1)
				go func(slot int, write func() error) {
					defer wg.Done()
					errs[slot] = write()
				}(slot, write)
				slot++
			}
		}
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d error = %v", i, err)
		}
	}

	if got := len(loadSelections()); got != rounds*writers {
		t.Errorf("len(loadSelections()) = %d, want %d", got, rounds*writers)
	}
	if got := loadProjectOpens()["/repo/shared"]; got != rounds*writers {
		t.Errorf("loadProjectOpens()[/repo/shared] = %d, want %d", got, rounds*writers)
	}
	if got := loadSidebarPct(); got < 20 || got >= 20+rounds {
		t.Errorf("loadSidebarPct() = %d, want one of the written values in [20,%d)", got, 20+rounds)
	}
}

// TestRecordProjectOpenSerializesConcurrentOpens covers recordProjectOpen —
// the closure dashDeps hands to ui.Deps.RecordProjectOpen — through the same
// selectionWriteMu that guards rememberSelection.
//
// The "concurrent increments of the SAME key" subtest is the load-bearing
// one, and it is a stricter probe than disjoint-key selection writes: every
// RecordOpen call is itself a read-modify-write of the count (Opened[k]++),
// so even two writers targeting one key race unless every call is fully
// serialized, not just non-overlapping. Without the lock this loses
// increments on nearly every run; distinct keys per round (as in the
// selection test above) would only catch a DIFFERENT bug, so this test
// deliberately reuses one key across all writers and rounds and checks the
// total against the exact number of calls made.
func TestRecordProjectOpenSerializesConcurrentOpens(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	t.Run("sequential opens accumulate", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			if err := recordProjectOpen("/repo/a"); err != nil {
				t.Fatalf("recordProjectOpen(/repo/a) call %d error = %v", i, err)
			}
		}
		got := loadProjectOpens()
		if got["/repo/a"] != 3 {
			t.Errorf("loadProjectOpens()[/repo/a] = %d, want 3", got["/repo/a"])
		}
	})

	t.Run("concurrent increments of the same key all survive", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())

		const writers = 8
		const rounds = 25
		total := 0
		for range rounds {
			var wg sync.WaitGroup
			errs := make([]error, writers)
			for i := range writers {
				wg.Add(1)
				total++
				go func(i int) {
					defer wg.Done()
					errs[i] = recordProjectOpen("/repo/shared")
				}(i)
			}
			wg.Wait()
			for i, err := range errs {
				if err != nil {
					t.Fatalf("writer %d: recordProjectOpen(/repo/shared) error = %v", i, err)
				}
			}
		}
		got := loadProjectOpens()
		if got["/repo/shared"] != total {
			t.Errorf("after %d rounds of %d concurrent writers on one key, loadProjectOpens()[/repo/shared] = %d, want %d",
				rounds, writers, got["/repo/shared"], total)
		}
	})
}

// TestRememberSelectionAndRecordProjectOpenShareTheWriteLock proves the two
// production write seams take the SAME selectionWriteMu rather than two
// independent locks: uistate.Remember and uistate.RecordOpen each
// read-modify-write-rename the whole shared state file, so a Selected write
// racing an Opened write through separate locks would let whichever renames
// last silently discard the other's field — exactly the collision
// selectionWriteMu's comment documents. Interleaving both seams across many
// goroutines and checking that every Selected entry AND every Opened
// increment survived is what a second, unshared mutex would fail: it
// serializes each seam against itself but not against the other.
func TestRememberSelectionAndRecordProjectOpenShareTheWriteLock(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	const writers = 8
	const rounds = 25
	wantSelected := map[string]string{}
	wantOpenedTotal := 0
	for round := range rounds {
		var wg sync.WaitGroup
		errs := make([]error, writers*2)
		for i := range writers {
			key := fmt.Sprintf("/repo/%d/%d", round, i)
			name := fmt.Sprintf("session-%d-%d", round, i)
			wantSelected[key] = name
			wantOpenedTotal++

			wg.Add(2)
			go func(slot int) {
				defer wg.Done()
				errs[slot] = rememberSelection(key, name)
			}(i * 2)
			go func(slot int) {
				defer wg.Done()
				errs[slot] = recordProjectOpen("/repo/shared")
			}(i*2 + 1)
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d: writer %d error = %v", round, i, err)
			}
		}
	}

	gotSelected := loadSelections()
	if !maps.Equal(gotSelected, wantSelected) {
		t.Errorf("after interleaved writes, loadSelections() = %v, want %v", gotSelected, wantSelected)
	}
	gotOpened := loadProjectOpens()
	if gotOpened["/repo/shared"] != wantOpenedTotal {
		t.Errorf("after interleaved writes, loadProjectOpens()[/repo/shared] = %d, want %d",
			gotOpened["/repo/shared"], wantOpenedTotal)
	}
}

// TestConfiguredLeader pins the start-up read of config.toml's leader. The
// load-error and bad-[dash] rows are load-bearing: both must give "" (the
// default leader) without failing, because a malformed file must not stop the
// dashboard; the bad-[dash] row also proves a leader sitting beside a bad
// value is not used.
func TestConfiguredLeader(t *testing.T) {
	tests := []struct {
		name string
		file string
		want string
	}{
		{name: "absent file", file: "", want: ""},
		{name: "value present", file: "[dash]\nleader = \"ctrl+o\"\n", want: "ctrl+o"},
		{name: "no leader key", file: "[dash]\nmax_depth = 3\n", want: ""},
		{name: "bad dash value", file: "[dash]\nleader = \"ctrl+o\"\nscan_interval = \"soon\"\n", want: ""},
		{name: "load error", file: "[dash\n", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			d := daemonconfig.DefaultDeps()
			d.Getenv = func(k string) string {
				if k == "XDG_CONFIG_HOME" {
					return dir
				}
				return ""
			}
			if tt.file != "" {
				if err := os.MkdirAll(filepath.Join(dir, "afk"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(daemonconfig.Path(d), []byte(tt.file), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := configuredLeader(d); got != tt.want {
				t.Errorf("configuredLeader = %q, want %q", got, tt.want)
			}
		})
	}
}
