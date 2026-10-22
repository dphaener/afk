package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dphaener/afk/internal/daemonconfig"
	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/projectrows"
	"github.com/dphaener/afk/internal/provider"
	"github.com/dphaener/afk/internal/usagestate"
	"github.com/dphaener/afk/internal/web"
)

// dashAllProjectsTestParts reads config.toml from cfgHome, with no git, no
// real review list and a session lister that records its calls.
func dashAllProjectsTestParts(t *testing.T, cfgHome string, reviews []daemonstate.Review, sessionCalls *[]string) dashAllProjectsParts {
	t.Helper()
	return dashAllProjectsParts{
		cfg: daemonconfig.Deps{
			Getenv:   func(k string) string { return map[string]string{"XDG_CONFIG_HOME": cfgHome}[k] },
			HomeDir:  func() (string, error) { return "", errors.New("no home") },
			ReadFile: os.ReadFile,
		},
		isDir:        func(p string) bool { fi, err := os.Stat(p); return err == nil && fi.IsDir() },
		gitCommonDir: func(string) string { return "" },
		evalSymlinks: func(p string) (string, error) { return p, nil },
		reviews:      func() []daemonstate.Review { return reviews },
		sessions: func(root string) []web.SessionView {
			*sessionCalls = append(*sessionCalls, root)
			return []web.SessionView{{Name: "t1-2", Phase: "executing"}}
		},
		now:   func() time.Time { return dashBuildNow },
		usage: func() (usagestate.File, bool) { return usagestate.File{}, false },
	}
}

func writeDashAllProjectsConfig(t *testing.T, cfgHome string, projects map[string]string, order []string) {
	t.Helper()
	var b strings.Builder
	for _, name := range order {
		fmt.Fprintf(&b, "[[project]]\nname = %q\npath = %q\n\n", name, projects[name])
	}
	writeBuildFile(t, cfgHome, "afk/config.toml", b.String())
}

// TestLoadDashAllProjects reads every project of one config.toml in a temp
// folder. Load-bearing rows: "one broken ledger" proves a project that cannot
// be read is its own row and the others still come back; "gone folder" proves
// a missing folder gets a row and no read; "no ledger running task" proves
// discovery is asked for only a project with a running task; "unreadable
// config" proves the config failure comes back as an error with no rows, so
// the dashboard keeps the rows it had.
func TestLoadDashAllProjects(t *testing.T) {
	type built struct {
		cfgHome string
		active  string
		reviews []daemonstate.Review
		roots   map[string]string
	}
	cases := []struct {
		name  string
		setup func(t *testing.T) built
		// want is "name state" per row in order; extra checks follow.
		want         []string
		wantErr      string
		wantSessions []string // names of projects whose sessions were listed
		check        func(t *testing.T, b built, rows []projectrows.Row)
	}{
		{
			name: "running project",
			setup: func(t *testing.T) built {
				a := t.TempDir()
				writeBuildFile(t, a, "docs/PLAN.md", dashBuildPlan)
				writeBuildFile(t, a, ".afk/build/ledger.json", dashBuildLedger)
				writeBuildFile(t, a, ".afk/build/daemon.json", `{"at":"2026-10-12T11:59:00Z","kind":"wait","task":"T1.2","session":"t1-2","reason":"running"}`)
				cfg := t.TempDir()
				writeDashAllProjectsConfig(t, cfg, map[string]string{"alpha": a}, []string{"alpha"})
				return built{cfgHome: cfg, roots: map[string]string{"alpha": a}}
			},
			want:         []string{"alpha running"},
			wantSessions: []string{"alpha"},
			check: func(t *testing.T, b built, rows []projectrows.Row) {
				r := rows[0]
				if r.Task != "T1.2" || r.Phase != "executing" || r.Done != 1 || r.Total != 4 {
					t.Errorf("row = %+v, want T1.2 executing 1/4", r)
				}
			},
		},
		{
			name: "paused with a blocking inbox item",
			setup: func(t *testing.T) built {
				b := t.TempDir()
				writeBuildFile(t, b, "docs/PLAN.md", dashBuildPlan)
				seedDashInbox(t, b)
				if err := daemonstate.Pause(b, dashBuildNow); err != nil {
					t.Fatal(err)
				}
				cfg := t.TempDir()
				writeDashAllProjectsConfig(t, cfg, map[string]string{"beta": b}, []string{"beta"})
				return built{cfgHome: cfg}
			},
			want: []string{"beta paused"},
			check: func(t *testing.T, b built, rows []projectrows.Row) {
				r := rows[0]
				if r.Open != 2 || r.Blocking != 1 || !r.NeedsOwner {
					t.Errorf("row = %+v, want 2 open, 1 blocking, needs owner", r)
				}
			},
		},
		{
			name: "stopped project",
			setup: func(t *testing.T) built {
				c := t.TempDir()
				writeBuildFile(t, c, "docs/PLAN.md", dashBuildPlan)
				if err := daemonstate.Stop(c, "T1.1", "CI kept failing.", dashBuildNow); err != nil {
					t.Fatal(err)
				}
				cfg := t.TempDir()
				writeDashAllProjectsConfig(t, cfg, map[string]string{"gamma": c}, []string{"gamma"})
				return built{cfgHome: cfg}
			},
			want: []string{"gamma stopped"},
			check: func(t *testing.T, b built, rows []projectrows.Row) {
				if !strings.Contains(rows[0].Detail, "after T1.1: CI kept failing") || !strings.Contains(rows[0].Detail, "afk resume gamma") {
					t.Errorf("detail = %q, want the stop words of afk daemon status", rows[0].Detail)
				}
			},
		},
		{
			name: "gone folder",
			setup: func(t *testing.T) built {
				gone := filepath.Join(t.TempDir(), "missing")
				cfg := t.TempDir()
				writeDashAllProjectsConfig(t, cfg, map[string]string{"delta": gone}, []string{"delta"})
				return built{cfgHome: cfg}
			},
			want: []string{"delta gone"},
		},
		{
			name: "one broken ledger",
			setup: func(t *testing.T) built {
				good := t.TempDir()
				writeBuildFile(t, good, "docs/PLAN.md", dashBuildPlan)
				bad := t.TempDir()
				writeBuildFile(t, bad, "docs/PLAN.md", dashBuildPlan)
				writeBuildFile(t, bad, ".afk/build/ledger.json", "{ not json")
				cfg := t.TempDir()
				writeDashAllProjectsConfig(t, cfg, map[string]string{"bad": bad, "good": good}, []string{"bad", "good"})
				return built{cfgHome: cfg}
			},
			want: []string{"bad can't read", "good nothing ready"},
			check: func(t *testing.T, b built, rows []projectrows.Row) {
				if !strings.Contains(rows[0].Detail, "ledger") || !rows[0].NeedsOwner {
					t.Errorf("unreadable row = %+v, want a detail naming the ledger", rows[0])
				}
			},
		},
		{
			name: "no ledger, no running task",
			setup: func(t *testing.T) built {
				e := t.TempDir()
				writeBuildFile(t, e, "docs/PLAN.md", dashBuildPlan)
				cfg := t.TempDir()
				writeDashAllProjectsConfig(t, cfg, map[string]string{"eps": e}, []string{"eps"})
				return built{cfgHome: cfg}
			},
			want:         []string{"eps nothing ready"},
			wantSessions: nil,
		},
		{
			name: "no plan",
			setup: func(t *testing.T) built {
				cfg := t.TempDir()
				writeDashAllProjectsConfig(t, cfg, map[string]string{"bare": t.TempDir()}, []string{"bare"})
				return built{cfgHome: cfg}
			},
			want: []string{"bare no plan"},
		},
		{
			name: "plan on the review list",
			setup: func(t *testing.T) built {
				f := t.TempDir()
				writeBuildFile(t, f, "docs/PLAN.md", dashBuildPlan)
				cfg := t.TempDir()
				writeDashAllProjectsConfig(t, cfg, map[string]string{"zeta": f}, []string{"zeta"})
				return built{cfgHome: cfg, reviews: []daemonstate.Review{{Name: "zeta", Root: f}}}
			},
			want: []string{"zeta nothing ready"},
			check: func(t *testing.T, b built, rows []projectrows.Row) {
				if !rows[0].PlanWaiting || !rows[0].NeedsOwner {
					t.Errorf("row = %+v, want plan waiting and needs owner", rows[0])
				}
			},
		},
		{
			name: "no listed project, only the active one",
			setup: func(t *testing.T) built {
				act := t.TempDir()
				writeBuildFile(t, act, "docs/PLAN.md", dashBuildPlan)
				return built{cfgHome: t.TempDir(), active: act}
			},
			want: nil, // filled below: one row named for the folder
			check: func(t *testing.T, b built, rows []projectrows.Row) {
				if len(rows) != 1 || rows[0].Name != filepath.Base(b.active) || rows[0].Root != b.active {
					t.Errorf("rows = %+v, want only the active project %s", rows, b.active)
				}
			},
		},
		{
			name: "active project unlisted comes last",
			setup: func(t *testing.T) built {
				act := t.TempDir()
				writeBuildFile(t, act, "docs/PLAN.md", dashBuildPlan)
				l := t.TempDir()
				writeBuildFile(t, l, "docs/PLAN.md", dashBuildPlan)
				cfg := t.TempDir()
				writeDashAllProjectsConfig(t, cfg, map[string]string{"listed": l}, []string{"listed"})
				return built{cfgHome: cfg, active: act}
			},
			check: func(t *testing.T, b built, rows []projectrows.Row) {
				if len(rows) != 2 || rows[0].Name != "listed" || rows[1].Root != b.active || rows[1].Order <= rows[0].Order {
					t.Errorf("rows = %+v, want the listed project then the active one with a larger order", rows)
				}
				// Listed gates the remove action: only a config.toml entry can be removed.
				if !rows[0].Listed || rows[1].Listed {
					t.Errorf("Listed = %v, %v, want true for the config entry and false for the start-up project", rows[0].Listed, rows[1].Listed)
				}
			},
		},
		{
			name: "active project that is listed is one row",
			setup: func(t *testing.T) built {
				l := t.TempDir()
				writeBuildFile(t, l, "docs/PLAN.md", dashBuildPlan)
				cfg := t.TempDir()
				writeDashAllProjectsConfig(t, cfg, map[string]string{"both": l}, []string{"both"})
				return built{cfgHome: cfg, active: l}
			},
			want: []string{"both nothing ready"},
		},
		{
			name: "unreadable config",
			setup: func(t *testing.T) built {
				cfg := t.TempDir()
				writeBuildFile(t, cfg, "afk/config.toml", "[[project\nbroken")
				return built{cfgHome: cfg}
			},
			wantErr: "config.toml",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			b := tt.setup(t)
			var calls []string
			parts := dashAllProjectsTestParts(t, b.cfgHome, b.reviews, &calls)
			got := loadDashAllProjects(parts, b.active)
			if got.ConfigPath != filepath.Join(b.cfgHome, "afk", "config.toml") {
				t.Errorf("ConfigPath = %q", got.ConfigPath)
			}
			if tt.wantErr != "" {
				if got.Err == nil || !strings.Contains(got.Err.Error(), tt.wantErr) || len(got.Rows) != 0 {
					t.Fatalf("got Err=%v rows=%d, want an error naming %q and no rows", got.Err, len(got.Rows), tt.wantErr)
				}
				return
			}
			if got.Err != nil {
				t.Fatalf("Err = %v", got.Err)
			}
			if tt.want != nil {
				var lines []string
				for _, r := range got.Rows {
					lines = append(lines, r.Name+" "+string(r.State))
				}
				if strings.Join(lines, "|") != strings.Join(tt.want, "|") {
					t.Errorf("rows = %v, want %v", lines, tt.want)
				}
			}
			if len(calls) != len(tt.wantSessions) {
				t.Errorf("sessions listed for %v, want %v", calls, tt.wantSessions)
			}
			if tt.check != nil {
				tt.check(t, b, got.Rows)
			}
		})
	}
}

// TestLoadDashAllProjectsWritesNothing is the read-only guard: the project's
// own folder is the same before and after, pause flag and all.
func TestLoadDashAllProjectsWritesNothing(t *testing.T) {
	root := t.TempDir()
	writeBuildFile(t, root, "docs/PLAN.md", dashBuildPlan)
	writeBuildFile(t, root, ".afk/build/ledger.json", dashBuildLedger)
	seedDashInbox(t, root)
	cfg := t.TempDir()
	writeDashAllProjectsConfig(t, cfg, map[string]string{"p": root}, []string{"p"})
	snapshot := func() string {
		var out []string
		_ = filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
			if err == nil && !fi.IsDir() {
				data, _ := os.ReadFile(p)
				out = append(out, p+"\x00"+string(data)+fi.ModTime().String())
			}
			return nil
		})
		return strings.Join(out, "\n")
	}
	before := snapshot()
	var calls []string
	loadDashAllProjects(dashAllProjectsTestParts(t, cfg, nil, &calls), "")
	if after := snapshot(); after != before {
		t.Error("loading the rows changed files in the project folder")
	}
}

// TestLoadDashAllProjectsWait: the older of two open blocking items wins and
// carries its task's title (load-bearing: the panel shows what the oldest
// question unblocks); non-blocking and answered items give no wait; a task id
// missing from the plan leaves TaskTitle empty without an error row.
func TestLoadDashAllProjectsWait(t *testing.T) {
	tests := []struct {
		name string
		adds []inbox.NewItem
		// answer is the ids (1-based position) to answer after adding.
		answer []int
		want   projectrows.Wait
	}{
		{"two_open_blocking_oldest_wins_with_task_title",
			[]inbox.NewItem{
				{Kind: "question", Blocking: true, Task: "T1.3", Title: "Old", Body: "older body"},
				{Kind: "question", Blocking: true, Task: "T1.1", Title: "Newer"},
				{Kind: "question", Blocking: true, Task: "T1.2", Title: "Answered"},
			}, []int{3},
			projectrows.Wait{ItemID: "aaaaa2", Kind: "question", Title: "Old", Body: "older body", Task: "T1.3", TaskTitle: "Third"}},
		{"only_non_blocking_gives_no_wait",
			[]inbox.NewItem{{Kind: "notice", Title: "FYI"}}, nil, projectrows.Wait{}},
		{"only_answered_gives_no_wait",
			[]inbox.NewItem{{Kind: "question", Blocking: true, Task: "T1.1", Title: "Done"}}, []int{1}, projectrows.Wait{}},
		{"task_missing_from_plan_leaves_title_empty",
			[]inbox.NewItem{{Kind: "question", Blocking: true, Task: "T9.9", Title: "Lost"}}, nil,
			projectrows.Wait{ItemID: "aaaaa2", Kind: "question", Title: "Lost", Task: "T9.9"}},
		{"no_task_named",
			[]inbox.NewItem{{Kind: "question", Blocking: true, Title: "General"}}, nil,
			projectrows.Wait{ItemID: "aaaaa2", Kind: "question", Title: "General"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeBuildFile(t, root, "docs/PLAN.md", dashBuildPlan)
			ids3 := []string{"aaaaa2", "bbbbb3", "ccccc4"}
			tick := 0
			store := inbox.Store{
				Dir:   inbox.Dir(root),
				Now:   func() time.Time { tick++; return dashBuildNow.Add(time.Duration(tick) * time.Minute) },
				NewID: func() string { id := ids3[0]; ids3 = ids3[1:]; return id },
			}
			var ids []string
			for _, a := range tt.adds {
				it, err := store.Add(a)
				if err != nil {
					t.Fatal(err)
				}
				ids = append(ids, it.ID)
			}
			for _, i := range tt.answer {
				if _, err := store.Answer(ids[i-1], "ok", "cli"); err != nil {
					t.Fatal(err)
				}
			}
			cfg := t.TempDir()
			writeDashAllProjectsConfig(t, cfg, map[string]string{"p": root}, []string{"p"})
			var calls []string
			got := loadDashAllProjects(dashAllProjectsTestParts(t, cfg, nil, &calls), "")
			if got.Err != nil || len(got.Rows) != 1 {
				t.Fatalf("Err=%v rows=%d", got.Err, len(got.Rows))
			}
			r := got.Rows[0]
			if r.State == projectrows.StateUnreadable {
				t.Fatalf("row is unreadable: %q", r.Detail)
			}
			w := r.Wait
			w.CreatedAt = "" // the clock is fake and only the order matters
			if w != tt.want {
				t.Errorf("Wait = %+v, want %+v", w, tt.want)
			}
			if tt.want.ItemID != "" && r.Wait.CreatedAt == "" {
				t.Error("Wait.CreatedAt is empty, want the item's creation time")
			}
		})
	}
}

// TestDashLoadAllProjectsTasks pins the task lists the jump search is built
// from. Load-bearing rows: "ledger statuses" (a task with no ledger entry is
// pending, and the status is the ledger's word, not a guess), "unreadable
// ledger" (a project whose files cannot be read lists no tasks rather than
// stale ones), "no plan" and "gone folder" (no entry at all), and "active
// project unlisted" (its tasks come back too, keyed by its root).
func TestDashLoadAllProjectsTasks(t *testing.T) {
	cfg := t.TempDir()
	good := t.TempDir()
	writeBuildFile(t, good, "docs/PLAN.md", dashBuildPlan)
	writeBuildFile(t, good, ".afk/build/ledger.json", dashBuildLedger)
	bad := t.TempDir()
	writeBuildFile(t, bad, "docs/PLAN.md", dashBuildPlan)
	writeBuildFile(t, bad, ".afk/build/ledger.json", "{ not json")
	bare := t.TempDir()
	gone := filepath.Join(t.TempDir(), "missing")
	act := t.TempDir()
	writeBuildFile(t, act, "docs/PLAN.md", dashBuildPlan)
	writeDashAllProjectsConfig(t, cfg,
		map[string]string{"good": good, "bad": bad, "bare": bare, "gone": gone},
		[]string{"good", "bad", "bare", "gone"})

	var calls []string
	got := loadDashAllProjects(dashAllProjectsTestParts(t, cfg, nil, &calls), act)
	if got.Err != nil {
		t.Fatal(got.Err)
	}

	t.Run("ledger statuses", func(t *testing.T) {
		var lines []string
		for _, jt := range got.Tasks[good] {
			lines = append(lines, jt.ID+"="+jt.Status)
		}
		want := "T1.1=done|T1.2=running|T1.3=parked|T1.4=pending"
		if strings.Join(lines, "|") != want {
			t.Errorf("tasks = %v, want %s", lines, want)
		}
		if first := got.Tasks[good][0]; first.Title != "First" {
			t.Errorf("title = %q, want First", first.Title)
		}
	})
	for name, root := range map[string]string{"unreadable ledger": bad, "no plan": bare, "gone folder": gone} {
		t.Run(name, func(t *testing.T) {
			if jt, ok := got.Tasks[root]; ok {
				t.Errorf("tasks = %v, want no entry", jt)
			}
		})
	}
	t.Run("active project unlisted", func(t *testing.T) {
		if len(got.Tasks[act]) != 4 {
			t.Errorf("tasks = %v, want the four of the plan", got.Tasks[act])
		}
	})
}

// TestReadDashProjectRowTasks pins the return value of readDashProjectRow:
// the tasks only when the row could be filled in whole.
func TestReadDashProjectRowTasks(t *testing.T) {
	var calls []string
	p := dashAllProjectsTestParts(t, t.TempDir(), nil, &calls)
	root := t.TempDir()
	writeBuildFile(t, root, "docs/PLAN.md", dashBuildPlan)
	in := projectrows.Input{Name: "p", Root: root}
	if jt := readDashProjectRow(p, &in, nil); len(jt) != 4 {
		t.Errorf("tasks = %v, want 4", jt)
	}
	bare := projectrows.Input{Name: "b", Root: t.TempDir()}
	if jt := readDashProjectRow(p, &bare, nil); jt != nil {
		t.Errorf("tasks = %v, want nil for a project with no plan", jt)
	}
}

// TestLoadDashAllProjectsUsage pins that the machine's usage reading rides
// along with the every-project read (T16.9 F7), so home shows usage when the
// dashboard starts outside a project. Load-bearing: no reading stays unknown.
func TestLoadDashAllProjectsUsage(t *testing.T) {
	f := usagestate.File{Windows: []usagestate.Window{{Kind: provider.WindowWeekly, UsedPct: 41}}}
	cases := []struct {
		name  string
		usage func() (usagestate.File, bool)
		known bool
	}{
		{"reading passes through", func() (usagestate.File, bool) { return f, true }, true},
		{"no reading", func() (usagestate.File, bool) { return usagestate.File{}, false }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfgHome := t.TempDir()
			writeDashAllProjectsConfig(t, cfgHome, nil, nil)
			var calls []string
			p := dashAllProjectsTestParts(t, cfgHome, nil, &calls)
			p.usage = tc.usage
			got := loadDashAllProjects(p, "")
			if got.UsageKnown != tc.known || (tc.known && (len(got.Usage.Windows) != 1 || got.Usage.Windows[0].UsedPct != 41)) {
				t.Fatalf("usage = %+v known %v, want known %v", got.Usage, got.UsageKnown, tc.known)
			}
		})
	}
}
