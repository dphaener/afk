package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/buildview"
	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/ui"
	"github.com/dphaener/afk/internal/usagestate"
)

const dashBuildPlan = `# Plan

### Milestone 1

#### T1.1 — First

- **Depends on:** none

#### T1.2 — Second

- **Depends on:** T1.1

#### T1.3 — Third

- **Depends on:** none

#### T1.4 — Fourth

- **Depends on:** T1.2
`

// dashBuildLedger has one task done, one running in session t1-2 and one
// parked on the inbox item aaaaa2. T1.4 has no entry, so it is pending.
const dashBuildLedger = `{"tasks":[
 {"id":"T1.1","status":"done","pr":7,"merged_sha":"abcdef1234","finished_at":"2026-10-12T08:00:00Z"},
 {"id":"T1.2","status":"running","session":"t1-2"},
 {"id":"T1.3","status":"parked","parked_on":["aaaaa2"]}
]}
`

var dashBuildNow = time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)

// dashBuildUsage is a reading a test can tell apart from "no reading".
func dashBuildUsage() usagestate.File {
	return usagestate.File{
		At:      dashBuildNow,
		Windows: []usagestate.Window{{Kind: "weekly_all", UsedPct: 91}},
		Source:  usagestate.SourceProbe,
	}
}

// dashBuildTestParts are the loader's outside lookups with nothing real in
// them: no git, a fixed usage reading, a fixed clock and UTC. commonDir is
// what git would answer for any folder ("" for not a repository).
func dashBuildTestParts(commonDir string) dashBuildParts {
	return dashBuildParts{
		gitCommonDir: func(string) string { return commonDir },
		usage:        func() (usagestate.File, bool) { return dashBuildUsage(), true },
		now:          func() time.Time { return dashBuildNow },
		loc:          time.UTC,
	}
}

// seedDashInbox writes two items into root's inbox with known ids: aaaaa2, a
// question that stops its task, and bbbbb3, a notice.
func seedDashInbox(t *testing.T, root string) {
	t.Helper()
	ids := []string{"aaaaa2", "bbbbb3"}
	store := inbox.Store{
		Dir:   inbox.Dir(root),
		Now:   func() time.Time { return dashBuildNow },
		NewID: func() string { id := ids[0]; ids = ids[1:]; return id },
	}
	for _, n := range []inbox.NewItem{
		{Kind: "question", Blocking: true, Task: "T1.3", Title: "Which database?", Options: []string{"Postgres", "SQLite"}},
		{Kind: "notice", Title: "The deploy took nine minutes"},
	} {
		if _, err := store.Add(n); err != nil {
			t.Fatalf("seeding the inbox: %v", err)
		}
	}
}

func dashItemIDs(items []inbox.Item) []string {
	var ids []string
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	return ids
}

func dashRowText(rows []buildview.Row) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.ID+" "+r.Status+" "+r.Detail)
	}
	return out
}

// TestDashBuildLoad is the table for the dashboard's real build loader, over
// temp folders. Every row also checks that the inbox and the usage reading
// are filled in, because the loader must not let a plan or ledger fault take
// those down with it. The load-bearing rows:
//
//   - "a linked worktree resolves to the main checkout" is the reason the
//     loader does its own lookup. It uses real git on a real linked worktree:
//     a dashboard started inside a session would otherwise read the
//     worktree's own empty .afk/ and show a project with no build.
//   - "no plan and no ledger" must be HasBuild false with NO problem. Most
//     projects a person opens in the dashboard are not built by afk, and a
//     red sentence on each of them would teach the reader to ignore it.
//   - "a plan that does not parse" and "a damaged ledger" must name the file
//     in Problem and give no rows: rows worked out from half the data would
//     state as fact a status nobody recorded.
//   - "a ledger and no plan" is a problem and not "no build": the ledger says
//     afk has been building here, so a missing plan is a fault to report.
//   - "a stale session's phase is not shown" guards against a dead session's
//     last phase being read as what the task is doing now.
func TestDashBuildLoad(t *testing.T) {
	// plain makes a project folder that is not a git repository, which is
	// enough for every row that is not about the checkout lookup.
	plain := func(t *testing.T, files map[string]string) string {
		t.Helper()
		root := t.TempDir()
		for rel, content := range files {
			writeBuildFile(t, root, rel, content)
		}
		seedDashInbox(t, root)
		return root
	}
	executing := []ui.BuildSession{{Name: "t1-2", Phase: "executing", HostRunning: true}}

	tests := []struct {
		name string
		// setup returns the folder the dashboard is showing, the main
		// checkout the loader must end up reading, and the lookups to use.
		setup    func(t *testing.T) (repo, wantRoot string, parts dashBuildParts)
		sessions []ui.BuildSession

		wantHasBuild bool
		// wantProblem is a list of pieces the problem sentence must hold; nil
		// means there must be no problem.
		wantProblem []string
		wantRows    []string
		wantDone    int
		wantTotal   int
		wantCurrent string
		wantPhase   string
		wantItems   []string
		check       func(t *testing.T, snap buildview.Snapshot)
	}{
		{
			name: "a project with a plan and a ledger",
			setup: func(t *testing.T) (string, string, dashBuildParts) {
				root := plain(t, map[string]string{
					"docs/PLAN.md":           dashBuildPlan,
					".afk/build/ledger.json": dashBuildLedger,
					".afk/build/daemon.json": `{"at":"2026-10-12T11:59:00Z","kind":"wait","task":"T1.2","session":"t1-2","reason":"running"}`,
					// An item file that cannot be read is counted, not dropped.
					".afk/inbox/ccccc4.json": "{ this is not JSON",
				})
				return root, root, dashBuildTestParts("")
			},
			sessions:     executing,
			wantHasBuild: true,
			wantRows: []string{
				"T1.1 done PR #7 · abcdef1",
				"T1.2 running t1-2 · executing",
				"T1.3 parked waiting on you: aaaaa2",
				"T1.4 pending waiting for T1.2",
			},
			wantDone:    1,
			wantTotal:   4,
			wantCurrent: "T1.2",
			wantPhase:   "executing",
			wantItems:   []string{"aaaaa2", "bbbbb3"},
			check: func(t *testing.T, snap buildview.Snapshot) {
				if snap.Unreadable != 1 {
					t.Errorf("Unreadable = %d, want 1 for the one damaged item file", snap.Unreadable)
				}
				if snap.Paused || snap.Stopped {
					t.Errorf("Paused = %v, Stopped = %v, want neither", snap.Paused, snap.Stopped)
				}
				if !strings.Contains(snap.LastShip, "T1.1 shipped") {
					t.Errorf("LastShip = %q, want it to say T1.1 shipped", snap.LastShip)
				}
				if len(snap.PausedSessions) != 0 {
					t.Errorf("PausedSessions = %v, want none", snap.PausedSessions)
				}
			},
		},
		{
			name: "a linked worktree resolves to the main checkout",
			setup: func(t *testing.T) (string, string, dashBuildParts) {
				main := newTestRepo(t)
				writeBuildFile(t, main, "docs/PLAN.md", dashBuildPlan)
				writeBuildFile(t, main, ".afk/build/ledger.json", dashBuildLedger)
				seedDashInbox(t, main)
				wt := addWorktree(t, main, "t1-2")
				parts := dashBuildTestParts("")
				parts.gitCommonDir = resolveGitCommonDirIn
				return wt, main, parts
			},
			sessions:     executing,
			wantHasBuild: true,
			wantRows: []string{
				"T1.1 done PR #7 · abcdef1",
				"T1.2 running t1-2 · executing",
				"T1.3 parked waiting on you: aaaaa2",
				"T1.4 pending waiting for T1.2",
			},
			wantDone:    1,
			wantTotal:   4,
			wantCurrent: "T1.2",
			wantPhase:   "executing",
			wantItems:   []string{"aaaaa2", "bbbbb3"},
		},
		{
			name: "a subfolder of the main checkout resolves to its top",
			setup: func(t *testing.T) (string, string, dashBuildParts) {
				root := plain(t, map[string]string{
					"docs/PLAN.md":   dashBuildPlan,
					"cmd/tool/x.txt": "x",
				})
				return filepath.Join(root, "cmd", "tool"), root, dashBuildTestParts(filepath.Join(root, ".git"))
			},
			wantHasBuild: true,
			wantRows: []string{
				"T1.1 pending ready",
				"T1.2 pending waiting for T1.1",
				"T1.3 pending ready",
				"T1.4 pending waiting for T1.2",
			},
			wantTotal: 4,
			wantItems: []string{"aaaaa2", "bbbbb3"},
		},
		{
			name: "no plan and no ledger",
			setup: func(t *testing.T) (string, string, dashBuildParts) {
				root := plain(t, nil)
				return root, root, dashBuildTestParts("")
			},
			sessions:     executing,
			wantHasBuild: false,
			wantItems:    []string{"aaaaa2", "bbbbb3"},
			check: func(t *testing.T, snap buildview.Snapshot) {
				if snap.LastShip != "" || snap.PausedSessions != nil {
					t.Errorf("a project with no build has LastShip %q and PausedSessions %v, want both empty", snap.LastShip, snap.PausedSessions)
				}
			},
		},
		{
			name: "a plan that does not parse",
			setup: func(t *testing.T) (string, string, dashBuildParts) {
				root := plain(t, map[string]string{
					"docs/PLAN.md":           "#### T1.1 — A\n- **Depends on:** T1.2\n\n#### T1.2 — B\n- **Depends on:** T1.1\n",
					".afk/build/ledger.json": dashBuildLedger,
				})
				return root, root, dashBuildTestParts("")
			},
			sessions:     executing,
			wantHasBuild: true,
			wantProblem:  []string{"The master plan", filepath.Join("docs", "PLAN.md"), "Fix the plan and try again."},
			wantItems:    []string{"aaaaa2", "bbbbb3"},
		},
		{
			name: "a damaged ledger",
			setup: func(t *testing.T) (string, string, dashBuildParts) {
				root := plain(t, map[string]string{
					"docs/PLAN.md":           dashBuildPlan,
					".afk/build/ledger.json": `{"tasks": [`,
				})
				return root, root, dashBuildTestParts("")
			},
			sessions:     executing,
			wantHasBuild: true,
			wantProblem:  []string{"Ledger file is damaged", filepath.Join(".afk", "build", "ledger.json"), "Fix or delete"},
			wantItems:    []string{"aaaaa2", "bbbbb3"},
		},
		{
			name: "a ledger and no plan",
			setup: func(t *testing.T) (string, string, dashBuildParts) {
				root := plain(t, map[string]string{".afk/build/ledger.json": dashBuildLedger})
				return root, root, dashBuildTestParts("")
			},
			wantHasBuild: true,
			wantProblem:  []string{"No master plan at", filepath.Join("docs", "PLAN.md")},
			wantItems:    []string{"aaaaa2", "bbbbb3"},
		},
		{
			name: "an afk.toml that cannot be read",
			setup: func(t *testing.T) (string, string, dashBuildParts) {
				root := plain(t, map[string]string{"afk.toml": "plan = [not toml\n"})
				return root, root, dashBuildTestParts("")
			},
			wantHasBuild: true,
			wantProblem:  []string{"afk.toml", "is not valid TOML"},
			wantItems:    []string{"aaaaa2", "bbbbb3"},
		},
		{
			name: "the plan afk.toml names is the one read",
			setup: func(t *testing.T) (string, string, dashBuildParts) {
				root := plain(t, map[string]string{
					"afk.toml":       "plan = \"notes/build.md\"\n",
					"notes/build.md": "#### T9.1 — Only\n- **Depends on:** none\n",
					// The default plan is there too and must not be the one read.
					"docs/PLAN.md": dashBuildPlan,
				})
				return root, root, dashBuildTestParts("")
			},
			wantHasBuild: true,
			wantRows:     []string{"T9.1 pending ready"},
			wantTotal:    1,
			wantItems:    []string{"aaaaa2", "bbbbb3"},
		},
		{
			name: "paused and stopped are read from their markers",
			setup: func(t *testing.T) (string, string, dashBuildParts) {
				root := plain(t, map[string]string{"docs/PLAN.md": dashBuildPlan})
				if err := daemonstate.Pause(root, dashBuildNow); err != nil {
					t.Fatal(err)
				}
				if err := daemonstate.Stop(root, "T1.1", "the deploy check failed", dashBuildNow); err != nil {
					t.Fatal(err)
				}
				return root, root, dashBuildTestParts("")
			},
			wantHasBuild: true,
			wantRows: []string{
				"T1.1 pending ready",
				"T1.2 pending waiting for T1.1",
				"T1.3 pending ready",
				"T1.4 pending waiting for T1.2",
			},
			wantTotal: 4,
			wantItems: []string{"aaaaa2", "bbbbb3"},
			check: func(t *testing.T, snap buildview.Snapshot) {
				if !snap.Paused || !snap.Stopped {
					t.Fatalf("Paused = %v, Stopped = %v, want both", snap.Paused, snap.Stopped)
				}
				if snap.StopTask != "T1.1" || snap.StopReason != "the deploy check failed" {
					t.Errorf("stop = %q, %q; want T1.1 and its reason", snap.StopTask, snap.StopReason)
				}
			},
		},
		{
			name: "stopped still shows beside a problem",
			setup: func(t *testing.T) (string, string, dashBuildParts) {
				root := plain(t, map[string]string{
					"docs/PLAN.md":           dashBuildPlan,
					".afk/build/ledger.json": "null",
				})
				if err := daemonstate.Stop(root, "T1.1", "the deploy check failed", dashBuildNow); err != nil {
					t.Fatal(err)
				}
				return root, root, dashBuildTestParts("")
			},
			wantHasBuild: true,
			wantProblem:  []string{"ledger.json"},
			wantItems:    []string{"aaaaa2", "bbbbb3"},
			check: func(t *testing.T, snap buildview.Snapshot) {
				if !snap.Stopped || snap.StopTask != "T1.1" {
					t.Errorf("Stopped = %v, StopTask = %q; want the stop marker read", snap.Stopped, snap.StopTask)
				}
			},
		},
		{
			name: "a stale session's phase is not shown",
			setup: func(t *testing.T) (string, string, dashBuildParts) {
				root := plain(t, map[string]string{
					"docs/PLAN.md":           dashBuildPlan,
					".afk/build/ledger.json": dashBuildLedger,
				})
				return root, root, dashBuildTestParts("")
			},
			sessions:     []ui.BuildSession{{Name: "t1-2", Phase: "executing", Stale: true}},
			wantHasBuild: true,
			wantRows: []string{
				"T1.1 done PR #7 · abcdef1",
				"T1.2 running t1-2",
				"T1.3 parked waiting on you: aaaaa2",
				"T1.4 pending waiting for T1.2",
			},
			wantDone:    1,
			wantTotal:   4,
			wantCurrent: "T1.2",
			wantPhase:   "unknown",
			wantItems:   []string{"aaaaa2", "bbbbb3"},
		},
		{
			name: "a session paused for usage, and a ship word shown with spaces",
			setup: func(t *testing.T) (string, string, dashBuildParts) {
				root := plain(t, map[string]string{
					"docs/PLAN.md": dashBuildPlan,
					".afk/build/ledger.json": `{"tasks":[
 {"id":"T1.1","status":"shipping","ship":"pr_open","pr":9},
 {"id":"T1.3","status":"running","session":"t1-3","usage_pause_at":"2026-10-12T11:00:00Z","usage_wake_at":"2026-10-12T18:00:00Z"}
]}`,
				})
				return root, root, dashBuildTestParts("")
			},
			sessions:     []ui.BuildSession{{Name: "t1-3", Phase: "executing", Stale: true}},
			wantHasBuild: true,
			wantRows: []string{
				"T1.1 shipping pr open · PR #9",
				"T1.2 pending waiting for T1.1",
				"T1.3 running t1-3 · paused for usage until Mon 12 Oct 18:00",
				"T1.4 pending waiting for T1.2",
			},
			wantTotal:   4,
			wantCurrent: "T1.1",
			wantPhase:   "pr open",
			wantItems:   []string{"aaaaa2", "bbbbb3"},
			check: func(t *testing.T, snap buildview.Snapshot) {
				want := map[string]string{"t1-3": "Mon 12 Oct 18:00"}
				if !reflect.DeepEqual(snap.PausedSessions, want) {
					t.Errorf("PausedSessions = %v, want %v", snap.PausedSessions, want)
				}
			},
		},
		{
			name: "an inbox folder that cannot be listed still shows the build",
			setup: func(t *testing.T) (string, string, dashBuildParts) {
				root := t.TempDir()
				writeBuildFile(t, root, "docs/PLAN.md", dashBuildPlan)
				// A file where the inbox folder should be.
				writeBuildFile(t, root, ".afk/inbox", "not a folder")
				return root, root, dashBuildTestParts("")
			},
			wantHasBuild: true,
			wantRows: []string{
				"T1.1 pending ready",
				"T1.2 pending waiting for T1.1",
				"T1.3 pending ready",
				"T1.4 pending waiting for T1.2",
			},
			wantTotal: 4,
			check: func(t *testing.T, snap buildview.Snapshot) {
				if snap.Unreadable != 1 {
					t.Errorf("Unreadable = %d, want 1 so the fault is on screen", snap.Unreadable)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo, wantRoot, parts := tc.setup(t)
			before := dashTreeListing(t, wantRoot)

			snap := loadDashBuild(parts, repo, tc.sessions)

			if snap.Root != wantRoot {
				t.Fatalf("Root = %q, want the main checkout %q (asked about %q)", snap.Root, wantRoot, repo)
			}
			if snap.HasBuild != tc.wantHasBuild {
				t.Fatalf("HasBuild = %v, want %v (Problem %q)", snap.HasBuild, tc.wantHasBuild, snap.Problem)
			}
			if tc.wantProblem == nil {
				if snap.Problem != "" {
					t.Fatalf("Problem = %q, want none", snap.Problem)
				}
			} else {
				// Shown under -v so the sentence the owner would read is on
				// record, with the temp folder cut out of it.
				t.Logf("Problem: %s (rows: %d, inbox items: %d)",
					strings.ReplaceAll(snap.Problem, wantRoot, "<project>"), len(snap.Rows), len(snap.Items))
				for _, piece := range tc.wantProblem {
					if !strings.Contains(snap.Problem, piece) {
						t.Errorf("Problem = %q, want it to hold %q", snap.Problem, piece)
					}
				}
				if !strings.HasSuffix(snap.Problem, ".") || strings.ToUpper(snap.Problem[:1]) != snap.Problem[:1] {
					t.Errorf("Problem = %q, want a sentence: a capital first letter and a full stop", snap.Problem)
				}
			}
			if got := dashRowText(snap.Rows); !reflect.DeepEqual(got, tc.wantRows) {
				t.Errorf("Rows =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(tc.wantRows, "\n  "))
			}
			if snap.Done != tc.wantDone || snap.Total != tc.wantTotal {
				t.Errorf("Done/Total = %d/%d, want %d/%d", snap.Done, snap.Total, tc.wantDone, tc.wantTotal)
			}
			if snap.Current != tc.wantCurrent || snap.CurrentPhase != tc.wantPhase {
				t.Errorf("Current = %q (%q), want %q (%q)", snap.Current, snap.CurrentPhase, tc.wantCurrent, tc.wantPhase)
			}
			if got := dashItemIDs(snap.Items); !reflect.DeepEqual(got, tc.wantItems) {
				t.Errorf("Items = %v, want %v", got, tc.wantItems)
			}
			if !snap.UsageKnown || !reflect.DeepEqual(snap.Usage, dashBuildUsage()) {
				t.Errorf("Usage = %+v (known %v), want the reading passed in", snap.Usage, snap.UsageKnown)
			}
			if tc.check != nil {
				tc.check(t, snap)
			}
			// The loader is a reader. Any file it created or removed would
			// show here, the ledger and the pause flag above all.
			if after := dashTreeListing(t, wantRoot); !reflect.DeepEqual(before, after) {
				t.Errorf("the load changed the project's files:\nbefore %v\nafter  %v", before, after)
			}
		})
	}

	t.Run("no usage reading is passed on as unknown", func(t *testing.T) {
		root := plain(t, map[string]string{"docs/PLAN.md": dashBuildPlan})
		parts := dashBuildTestParts("")
		parts.usage = func() (usagestate.File, bool) { return usagestate.File{}, false }
		snap := loadDashBuild(parts, root, nil)
		if snap.UsageKnown {
			t.Fatal("UsageKnown = true with no reading")
		}
		if len(snap.Rows) != 4 {
			t.Fatalf("got %d rows, want the build still shown without a usage reading", len(snap.Rows))
		}
	})
}

// dashTreeListing is every file under root's .afk and its size, so a test can
// say the loader wrote nothing.
func dashTreeListing(t *testing.T, root string) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	err := filepath.Walk(filepath.Join(root, ".afk"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !info.IsDir() {
			out[path] = info.Size()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("listing %s: %v", root, err)
	}
	return out
}

// TestDashBuildSentence pins the one rewording the loader does. The row that ends
// in a bracket is load-bearing: it is how the damaged-ledger error ends, and
// a rule that only looked for a letter at the end would leave it without its
// full stop.
func TestDashBuildSentence(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"empty stays empty", "", ""},
		{"only spaces stays empty", "  \n", ""},
		{"lower case and no stop", "the master plan x has a problem", "The master plan x has a problem."},
		{"already a sentence", "Fix the syntax and try again.", "Fix the syntax and try again."},
		{"ends in a bracket", "fix or delete x (deleting it forgets every task)", "Fix or delete x (deleting it forgets every task)."},
		{"starts with a path", "/tmp/afk.toml is not valid", "/tmp/afk.toml is not valid."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := dashSentence(tc.in); got != tc.want {
				t.Fatalf("dashSentence(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestDashAnswerInbox is the table for the dashboard's one write. The
// load-bearing rows:
//
//   - "an answer is recorded as answered from the dashboard" is the contract
//     with every other reader of the inbox: answered_via says tui.
//   - "options that are themselves numbers" pins that the text is resolved
//     ONCE. Resolving it here and again in the store would turn 1 into the
//     first option, "3", and then that "3" into the third option.
//   - "the first answer is the one kept" protects an agent that has already
//     acted on an answer from finding it replaced.
//   - "nothing but the item is written" is the limit on this seam: no ledger,
//     no pause flag. The build loop un-parks the task itself.
func TestDashAnswerInbox(t *testing.T) {
	seed := func(t *testing.T, n inbox.NewItem) (root string) {
		t.Helper()
		root = t.TempDir()
		store := inbox.Store{
			Dir:   inbox.Dir(root),
			Now:   func() time.Time { return dashBuildNow },
			NewID: func() string { return "aaaaa2" },
		}
		if _, err := store.Add(n); err != nil {
			t.Fatalf("seeding the inbox: %v", err)
		}
		return root
	}
	question := inbox.NewItem{Kind: "question", Blocking: true, Title: "Which database?", Options: []string{"Postgres", "SQLite"}}

	tests := []struct {
		name string
		item inbox.NewItem
		// before runs after seeding and before the answer under test.
		before     func(t *testing.T, root string)
		id, text   string
		wantErr    error
		wantAnswer string
	}{
		{
			name: "an answer is recorded as answered from the dashboard",
			item: question, id: "aaaaa2", text: "  use MySQL  ",
			wantAnswer: "use MySQL",
		},
		{
			name: "a number picks that option",
			item: question, id: "aaaaa2", text: "2",
			wantAnswer: "SQLite",
		},
		{
			name: "a number out of range is kept as typed",
			item: question, id: "aaaaa2", text: "7",
			wantAnswer: "7",
		},
		{
			name: "options that are themselves numbers are resolved once",
			item: inbox.NewItem{Kind: "question", Title: "How many workers?", Options: []string{"3", "1", "2"}},
			id:   "aaaaa2", text: "1",
			wantAnswer: "3",
		},
		{
			name: "a number on an item with no options is the answer",
			item: inbox.NewItem{Kind: "notice", Title: "How many?"},
			id:   "aaaaa2", text: "2",
			wantAnswer: "2",
		},
		{
			name: "the first answer is the one kept",
			item: question, id: "aaaaa2", text: "2",
			before: func(t *testing.T, root string) {
				if _, err := (inbox.Store{Dir: inbox.Dir(root)}).Answer("aaaaa2", "1", inbox.ViaWeb); err != nil {
					t.Fatal(err)
				}
			},
			wantErr:    inbox.ErrAlreadyAnswered,
			wantAnswer: "Postgres",
		},
		{
			name: "an empty answer is refused",
			item: question, id: "aaaaa2", text: "   ",
			wantErr: inbox.ErrEmptyAnswer,
		},
		{
			name: "an item that is not there",
			item: question, id: "zzzzz9", text: "2",
			wantErr: inbox.ErrNotFound,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := seed(t, tc.item)
			if tc.before != nil {
				tc.before(t, root)
			}
			before := dashTreeListing(t, root)

			got, err := dashAnswerInbox(root, tc.id, tc.text)

			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("dashAnswerInbox error = %v, want %v", err, tc.wantErr)
			}
			stored, readErr := (inbox.Store{Dir: inbox.Dir(root)}).Get("aaaaa2")
			if readErr != nil {
				t.Fatalf("reading the item back: %v", readErr)
			}
			if stored.Answer != tc.wantAnswer {
				t.Errorf("stored answer = %q, want %q", stored.Answer, tc.wantAnswer)
			}
			if tc.wantErr == nil {
				// The literal word, not the constant: other readers of the
				// file match on it.
				if stored.AnsweredVia != "tui" {
					t.Errorf("answered_via = %q, want tui", stored.AnsweredVia)
				}
				if got.Answer != tc.wantAnswer || got.ID != "aaaaa2" {
					t.Errorf("returned item = %+v, want the stored one", got)
				}
				raw, err := os.ReadFile(filepath.Join(inbox.Dir(root), "aaaaa2.json"))
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(raw), `"answered_via": "tui"`) && !strings.Contains(string(raw), `"answered_via":"tui"`) {
					t.Errorf("the item file does not say it was answered from the dashboard:\n%s", raw)
				}
			}
			if errors.Is(tc.wantErr, inbox.ErrAlreadyAnswered) {
				if stored.AnsweredVia != inbox.ViaWeb {
					t.Errorf("answered_via = %q after a refused second answer, want the first one's (web)", stored.AnsweredVia)
				}
				if got.Answer != "Postgres" {
					t.Errorf("returned item answer = %q, want the answer that stands", got.Answer)
				}
			}

			// Nothing but the item (and the inbox's own lock) is written.
			after := dashTreeListing(t, root)
			for path := range after {
				if _, was := before[path]; was || filepath.Dir(path) == inbox.Dir(root) {
					continue
				}
				t.Errorf("the answer wrote %s, which is outside the inbox", path)
			}
			if ledger.Exists(root) {
				t.Error("the answer created a ledger")
			}
			if daemonstate.Paused(root) {
				t.Error("the answer set the pause flag")
			}
		})
	}
}

// TestDashDepsWiresTheBuildSeams is the guard for the wiring itself. Both
// seams are nil by default in internal/ui, on purpose, so a dashboard built
// without them runs with no build data and no test in that package notices.
// The call through deps.LoadBuild is load-bearing: it proves the field holds
// the real loader, not just any non-nil function.
func TestDashDepsWiresTheBuildSeams(t *testing.T) {
	deps := dashDeps("", dashLocation{}, nil, -1)
	if deps.LoadBuild == nil {
		t.Fatal("dashDeps(...).LoadBuild is nil, want dashLoadBuild")
	}
	if deps.AnswerInbox == nil {
		t.Fatal("dashDeps(...).AnswerInbox is nil, want dashAnswerInbox")
	}
	if deps.PauseBuild == nil {
		t.Fatal("dashDeps(...).PauseBuild is nil, want dashPauseBuild")
	}
	if deps.ResumeBuild == nil {
		t.Fatal("dashDeps(...).ResumeBuild is nil, want dashResumeBuild")
	}
	if deps.LoadPlanStatus == nil {
		t.Fatal("dashDeps(...).LoadPlanStatus is nil, want dashLoadPlanStatus")
	}
	if deps.LoadAllProjects == nil {
		t.Fatal("dashDeps(...).LoadAllProjects is nil, want dashLoadAllProjects")
	}

	// A scratch state home, so the real usage read finds no file and cannot
	// touch this machine's own.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	writeBuildFile(t, root, "docs/PLAN.md", dashBuildPlan)
	seedDashInbox(t, root)

	snap := deps.LoadBuild(root, nil)
	if !snap.HasBuild || len(snap.Rows) != 4 || snap.Root != root {
		t.Fatalf("LoadBuild through dashDeps = %+v, want the four-task build at %s", snap, root)
	}
	if snap.UsageKnown {
		t.Error("UsageKnown = true with an empty state home")
	}

	it, err := deps.AnswerInbox(snap.Root, "aaaaa2", "1")
	if err != nil || it.Answer != "Postgres" || it.AnsweredVia != inbox.ViaTUI {
		t.Fatalf("AnswerInbox through dashDeps = %+v, %v; want Postgres answered from the dashboard", it, err)
	}
}
