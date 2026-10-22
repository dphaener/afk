package main

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/dphaener/afk/internal/buildview"
	"github.com/dphaener/afk/internal/daemonconfig"
	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/discover"
	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/projectrows"
	"github.com/dphaener/afk/internal/ui"
	"github.com/dphaener/afk/internal/usagestate"
	"github.com/dphaener/afk/internal/web"
)

// This file is the real version of ui.Deps.LoadAllProjects: one row for every
// project in the machine's config.toml, plus the project the dashboard was
// started in when it is not listed. It lives here and not in internal/ui for
// the reason dash_build.go gives (the main checkout lookup and the plan and
// ledger loader are in this package), and the row words come from
// internal/projectrows so `afk daemon status` and the screen cannot differ.
//
// It only reads. It runs on the dashboard's refresh tick while the screen is
// open, so it never dials a session host or the network. The one discovery
// call (a git worktree listing plus files) is made only for a project whose
// ledger has a running task, because that is the only project whose phase is
// shown, exactly as the web page does it (daemon_web.go).

// dashAllProjectsParts are the lookups outside a project's own files,
// injected so the loader can be tested against temp folders.
type dashAllProjectsParts struct {
	cfg daemonconfig.Deps
	// isDir reports whether path is a folder that exists.
	isDir func(path string) bool
	// gitCommonDir is the git common dir as seen from dir, "" when dir is not
	// in a repository.
	gitCommonDir func(dir string) string
	evalSymlinks func(path string) (string, error)
	// reviews are the plans waiting for approval on this machine.
	reviews func() []daemonstate.Review
	// sessions are the sessions of the project at root, nil when they cannot
	// be listed (the phase then reads as unknown).
	sessions func(root string) []web.SessionView
	now      func() time.Time
}

func realDashAllProjectsParts() dashAllProjectsParts {
	listed := realDashProjectParts()
	return dashAllProjectsParts{
		cfg:          listed.cfg,
		isDir:        listed.isDir,
		gitCommonDir: listed.gitCommonDir,
		evalSymlinks: listed.evalSymlinks,
		reviews: func() []daemonstate.Review {
			dir, err := usagestate.Dir(usagestate.DefaultDeps())
			if err != nil {
				return nil
			}
			return daemonstate.ReadReviews(dir)
		},
		sessions: func(root string) []web.SessionView {
			found, err := discover.Discover(root, discover.DefaultDeps())
			if err != nil {
				return nil
			}
			return webSessionViewsWithStart(found)
		},
		now: time.Now,
	}
}

// dashLoadAllProjects returns ui.Deps.LoadAllProjects for the real dashboard.
// active is the main checkout of the project the dashboard was started in
// ("" when there is none); it
// gets a row even when it is not listed, so the screen is never empty.
func dashLoadAllProjects(active string) func() ui.AllProjects {
	return func() ui.AllProjects {
		return loadDashAllProjects(realDashAllProjectsParts(), active)
	}
}

// loadDashAllProjects reads config.toml and one row per project. An
// unreadable config.toml comes back as an error with no rows, so the screen
// keeps the rows it had; a project whose own files cannot be read is a row
// saying so and never stops the others.
func loadDashAllProjects(p dashAllProjectsParts, active string) ui.AllProjects {
	path := daemonconfig.Path(p.cfg)
	cfg, err := daemonconfig.Load(p.cfg)
	if err != nil {
		return ui.AllProjects{ConfigPath: path, Err: err}
	}

	reviews := reviewRoots(p.reviews())

	listed := make([]rowProject, 0, len(cfg.Projects))
	for _, lp := range cfg.Projects {
		listed = append(listed, rowProject{Name: lp.Name, Path: lp.Path})
	}
	rows, seen, tasks := buildListedRowsTasks(p, listed, reviews)

	if active != "" {
		root := mainCheckout(active, p.gitCommonDir(active))
		if resolved, err := p.evalSymlinks(root); err == nil {
			root = resolved
		}
		if !seen[root] {
			// The largest order, so it sorts last in its group.
			in := projectrows.Input{Name: filepath.Base(root), Root: root, Order: len(cfg.Projects)}
			if jt := readDashProjectRow(p, &in, reviews); len(jt) > 0 {
				tasks[root] = jt
			}
			rows = append(rows, projectrows.Build(in))
		}
	}
	return ui.AllProjects{Rows: rows, ConfigPath: path, Tasks: tasks}
}

// rowProject is one listed project as buildListedRows takes it: a name and the
// folder it was listed with. Its position in the slice is its Order.
type rowProject struct{ Name, Path string }

// reviewRoots is the set of folders whose plan is waiting for approval.
func reviewRoots(reviews []daemonstate.Review) map[string]bool {
	out := map[string]bool{}
	for _, rv := range reviews {
		out[filepath.Clean(rv.Root)] = true
	}
	return out
}

// buildListedRows is one row per listed project, in list order and not yet
// sorted, plus the set of roots it covered. It takes the list as a parameter
// so the dashboard (which reads config.toml) and the web page (which uses the
// daemon's last good list and must never read config.toml) share every
// per-project rule: the main checkout, symlink resolution, duplicate roots
// skipped first-wins, a missing folder as a row saying so, and the project's
// own files. A project whose files cannot be read is a row saying so and never
// stops the others.
func buildListedRows(p dashAllProjectsParts, projects []rowProject, reviews map[string]bool) ([]projectrows.Row, map[string]bool) {
	rows, seen, _ := buildListedRowsTasks(p, projects, reviews)
	return rows, seen
}

// buildListedRowsTasks is buildListedRows plus each project's master-plan
// tasks keyed by the row's Root, which only the dashboard's jump search uses
// (the web page keeps calling buildListedRows). The tasks come from the plan
// and ledger readDashProjectRow already loaded for the row.
func buildListedRowsTasks(p dashAllProjectsParts, projects []rowProject, reviews map[string]bool) ([]projectrows.Row, map[string]bool, map[string][]ui.JumpTask) {
	tasks := map[string][]ui.JumpTask{}
	var rows []projectrows.Row
	seen := map[string]bool{}
	for i, lp := range projects {
		in := projectrows.Input{Name: lp.Name, Listed: true, Order: i}
		if !p.isDir(lp.Path) {
			in.Root = filepath.Clean(lp.Path)
			in.Gone = true
		} else {
			root := mainCheckout(lp.Path, p.gitCommonDir(lp.Path))
			if resolved, err := p.evalSymlinks(root); err == nil {
				root = resolved
			}
			in.Root = root
		}
		// Two entries on one checkout are one project; the first wins, as in
		// loadDashListed and the daemon.
		if seen[in.Root] {
			continue
		}
		seen[in.Root] = true
		if !in.Gone {
			if jt := readDashProjectRow(p, &in, reviews); len(jt) > 0 {
				tasks[in.Root] = jt
			}
		}
		rows = append(rows, projectrows.Build(in))
	}
	return rows, seen, tasks
}

// readDashProjectRow fills in from the project's own files. It reads the same
// files loadDashBuild does, and summarizes with web.Summarize so the current
// task and the count are the ones the Build tab and the web page show. It
// returns the project's tasks for the jump search, nil whenever the plan or
// the inbox could not be read (the row then says why and lists no tasks).
func readDashProjectRow(p dashAllProjectsParts, in *projectrows.Input, reviews map[string]bool) []ui.JumpTask {
	root := in.Root
	in.PlanWaiting = reviews[filepath.Clean(root)]

	items, _, listErr := inbox.Store{Dir: inbox.Dir(root)}.List()
	in.Open, in.Blocking = buildview.OpenCounts(items)
	in.Wait = oldestBlockingWait(items)

	in.HasPlan = dashHasBuild(root)
	if !in.HasPlan {
		return nil
	}

	bs, loadErr := loadBuildState(root)
	stopTask, stopReason, stopped := daemonstate.ReadStop(root)
	state, stateKnown := daemonstate.ReadState(root)
	paused := daemonstate.Paused(root)
	in.Paused, in.Stopped, in.StopTask, in.StopReason = paused, stopped, stopTask, stopReason
	in.Kind, in.Reason, in.StateKnown = state.Kind, state.Reason, stateKnown

	if loadErr != nil {
		in.ReadErr = rowFirstLine(dashSentence(loadErr.Error()))
		return nil
	}
	if listErr != nil {
		in.ReadErr = "The inbox folder could not be read: " + rowFirstLine(listErr.Error())
		return nil
	}

	if in.Wait.Task != "" {
		for _, t := range bs.Tasks {
			if t.ID == in.Wait.Task {
				in.Wait.TaskTitle = t.Title
				break
			}
		}
	}

	var sessions []web.SessionView
	for _, t := range bs.Tasks {
		if bs.Ledger.Status(t.ID) == ledger.Running {
			sessions = p.sessions(root)
			break
		}
	}
	sum := web.Summarize(web.SummaryInput{
		Name:       in.Name,
		Tasks:      bs.Tasks,
		Ledger:     bs.Ledger,
		State:      state,
		StateKnown: stateKnown,
		Paused:     paused,
		Stopped:    stopped,
		StopTask:   stopTask,
		StopReason: stopReason,
		Sessions:   sessions,
		Items:      items,
		Now:        p.now(),
	})
	in.Done, in.Total = sum.Done, sum.Total
	if sum.Current != nil {
		in.Task, in.Title = sum.Current.Task, sum.Current.Title
		in.Phase = strings.ReplaceAll(sum.Current.Phase, "_", " ")
	}

	jump := make([]ui.JumpTask, 0, len(bs.Tasks))
	for _, t := range bs.Tasks {
		jump = append(jump, ui.JumpTask{ID: t.ID, Title: t.Title, Status: string(bs.Ledger.Status(t.ID))})
	}
	return jump
}

// oldestBlockingWait picks the open item that holds a task up and has waited
// longest, for the home screen's panel. It works on the items readDashProjectRow
// already listed, so it reads no file; the web page maps rows through
// web.rowView and never shows it. Ties on the creation time go to the smaller
// id, so the choice does not depend on the order the store listed them in.
// TaskTitle is filled later, once the plan has loaded.
func oldestBlockingWait(items []inbox.Item) projectrows.Wait {
	var best *inbox.Item
	for i := range items {
		it := &items[i]
		if !it.Open() || !it.Blocking {
			continue
		}
		if best == nil || it.CreatedAt < best.CreatedAt || (it.CreatedAt == best.CreatedAt && it.ID < best.ID) {
			best = it
		}
	}
	if best == nil {
		return projectrows.Wait{}
	}
	return projectrows.Wait{
		ItemID: best.ID, Kind: best.Kind, Title: best.Title, Body: best.Body,
		CreatedAt: best.CreatedAt, Task: best.Task,
	}
}

// rowFirstLine is s up to its first line break.
func rowFirstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
