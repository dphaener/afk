package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/dphaener/afk/internal/project"
)

// ListedProject is one entry of the machine's project list (the `[[project]]`
// tables in config.toml, the list `afk project list` prints) as the dashboard
// needs it. It is this package's own type rather than the settings reader's
// because the dashboard must not read that file itself: the read is the
// injected Deps.LoadListed seam, and everything here works on its result.
type ListedProject struct {
	// Path is the project's main checkout, resolved the way the folder search
	// and the start-up lookup resolve theirs (symlinks followed). The merge
	// matches rows by plain string equality and does no file work of its own,
	// so a path spelled differently here would show as a second row for one
	// project.
	Path string
	// Name is the name `afk project list` prints for the entry: its `name` key,
	// or the folder name when it has none.
	Name string
	// Gone means the listed folder no longer exists. The entry is carried rather
	// than dropped by the loader so the merge is the one place that decides a
	// gone folder gets no row, and a test can state that rule without a disk.
	Gone bool
}

// mergeInput is everything mergeProjects reads. The sources arrive at
// different times (the start-up row at launch, the listed set from its own
// small read, the found set only after a whole folder walk, a started folder
// when a "start a new plan" succeeds), so the model remembers the last good
// value of each and re-merges whenever one changes.
type mergeInput struct {
	// held is the list shown now. Only its Pinned rows are read: pinned-ness is
	// where a row came from (the folder the dashboard was started in), and no
	// later read can re-derive it.
	held []project.Project
	// launchMainCheckout is the main checkout of the start-up project. It
	// differs from the pinned row's own path only when the dashboard was started
	// inside a linked worktree; empty means "same as the path".
	launchMainCheckout string
	// listed is the project list as last read well.
	listed []ListedProject
	// found is what the last good folder search found.
	found []project.Project
	// started is every folder a new plan was started in from this dashboard
	// during this run (Model.startedProjects), each the main checkout the
	// start reported. Nothing on disk names these folders: they need not be
	// in config.toml or under a search folder, and `afk plan approve` is what
	// lists one later. Without this source the next scan or listed read would
	// drop the row the dashboard just switched to.
	started []string
}

// mergeProjects builds the switcher's list from its four producers of paths:
// the folder search, the machine's project list, the folders a new plan was
// started in this run, and the pinned start-up row. One row per path.
//
// Who stays:
//   - a found project stays only while the latest search still finds it;
//   - a listed project stays whether or not the search finds it, because the
//     owner named it and most machines have no search folders at all;
//   - a listed entry whose folder is gone gets no row;
//   - the pinned start-up row always stays, keeps Pinned and keeps its own path;
//   - a started folder stays for the rest of the run, whether or not anything
//     else finds it, and is never Pinned: pinned-ness means "the folder the
//     dashboard was started in", which a started folder is not.
//
// The start-up row and the list. When the pinned row's path is listed, that is
// simply one row. When the dashboard was started inside a linked worktree of a
// listed project, the pinned row's path is the worktree and the listed path is
// the main checkout; the two are one project, so the pinned row stands in for
// it: no row is made for the listed path (not even when the search finds that
// main checkout), and the pinned row takes the listed name. The pinned row
// keeps its path because the dashboard's active project is found by path, and
// swapping it for the main checkout would move the owner off the folder they
// opened. Only the FIRST pinned row is treated this way: launchMainCheckout
// describes the start-up project and there is one start-up row.
//
// Order is fixed for a given input: found projects in search order, then
// listed ones not already present in list order, then started ones not
// already present in the order they were started, then pinned rows not
// already present. sameProjectPaths compares by position to decide whether an open
// switcher must be rebuilt, so an order that moved between two calls with the
// same input would reset the owner's cursor on every read.
//
// Identity is plain string equality on the path, which is safe only because
// every producer resolves the same way: internal/scan resolves each root
// through filepath.EvalSymlinks and walks real directories below it, the
// start-up lookup runs `git rev-parse --show-toplevel` through EvalSymlinks,
// and the listed loader resolves each entry to its main checkout the same way;
// a started folder is the start's main checkout, which `afk dash` resolves
// through EvalSymlinks before handing it back for this reason.
// Re-resolving here is not an option, because Update performs no I/O.
//
// Names are computed over the whole merged set first, because a pinned or
// listed row can share a folder name with a found one and DisplayNames only
// widens labels inside a colliding set. A listed row then takes its listed
// name, so the switcher and `afk project list` use the same word for it; the
// other rows keep the labels just computed.
func mergeProjects(in mergeInput) []project.Project {
	// live is the listed set that can have rows: a gone folder is dropped here,
	// once, so nothing below can give it a row, a name or a stand-in. A path
	// listed twice counts once, and the first entry's name wins.
	live := make([]ListedProject, 0, len(in.listed))
	listedName := make(map[string]string, len(in.listed))
	isListed := make(map[string]bool, len(in.listed))
	for _, e := range in.listed {
		if e.Gone || e.Path == "" || isListed[e.Path] {
			continue
		}
		isListed[e.Path] = true
		listedName[e.Path] = e.Name
		live = append(live, e)
	}

	pinned := make(map[string]bool, len(in.held))
	// stoodInFor is the listed main checkout the start-up row stands in for, and
	// standIn is that row's own path. Both stay empty unless the dashboard was
	// started in a linked worktree of a listed project.
	stoodInFor, standIn := "", ""
	first := true
	for _, p := range in.held {
		if !p.Pinned || pinned[p.Path] {
			continue
		}
		pinned[p.Path] = true
		if !first {
			continue
		}
		first = false
		mc := in.launchMainCheckout
		if mc != "" && mc != p.Path && isListed[mc] && !isListed[p.Path] {
			stoodInFor, standIn = mc, p.Path
		}
	}

	merged := make([]project.Project, 0, len(in.found)+len(in.listed)+len(in.started)+len(pinned))
	seen := make(map[string]bool, cap(merged))
	add := func(p project.Project) {
		if p.Path == "" || seen[p.Path] || (stoodInFor != "" && p.Path == stoodInFor) {
			return
		}
		seen[p.Path] = true
		merged = append(merged, p)
	}
	for _, p := range in.found {
		// Pinned-ness describes where a row came from, not whether this search
		// happened to find it. Dropping the flag on a search hit would let a
		// later settings change discard the project the owner is standing in.
		add(project.Project{Path: p.Path, Pinned: p.Pinned || pinned[p.Path]})
	}
	for _, e := range live {
		add(project.Project{Path: e.Path, Pinned: pinned[e.Path]})
	}
	for _, path := range in.started {
		// pinned[path] only when the start-up row has this very path, and that
		// row is the start-up project, so it keeps the flag it already has.
		add(project.Project{Path: path, Pinned: pinned[path]})
	}
	for _, p := range in.held {
		if p.Pinned {
			add(project.Project{Path: p.Path, Pinned: true})
		}
	}
	if len(merged) == 0 {
		return nil
	}

	paths := make([]string, len(merged))
	for i, p := range merged {
		paths[i] = p.Path
	}
	names := project.DisplayNames(paths)
	for i := range merged {
		merged[i].Name = names[i]
		key := merged[i].Path
		if key == standIn && standIn != "" {
			key = stoodInFor
		}
		// An entry with no name keeps the computed label rather than a blank row.
		if name := listedName[key]; name != "" {
			merged[i].Name = name
		}
	}
	return merged
}

// foldListedRead decides what one read of the project list does to the listed
// set the model holds. A good read replaces it, which is how a project removed
// from the file leaves the switcher. A read that failed keeps the held set
// exactly as it was and returns the words to show: those projects still exist,
// only the file naming them is unreadable, and emptying the switcher over a
// typo would hide working projects.
//
// A missing file is not a failed read. The loader answers it with no entries
// and no error, the same as an empty list.
func foldListedRead(held, entries []ListedProject, path string, err error) (next []ListedProject, notice string) {
	if err != nil {
		return held, listedNoticeText(path, err)
	}
	return entries, ""
}

// listedNoticeText is the toast for a project list that could not be read. The
// settings reader's own errors already begin with the file's path; when this
// one does not, the file is named here, because "what do I fix" needs a file
// name. Newlines become ` · ` so a multi-line parse error stays on the toast's
// one line, as notifyScanError does. A trailing full stop is trimmed so the
// sentence that follows does not start after two.
func listedNoticeText(path string, err error) string {
	text := strings.TrimSpace(strings.ReplaceAll(err.Error(), "\n", " · "))
	text = strings.TrimRight(text, ".")
	if path != "" && !strings.Contains(text, path) {
		text = path + ": " + text
	}
	return "Could not read the project list: " + text + ". The switcher keeps the projects it had."
}

// unpinnedProjects is the part of the start-up list that plays the role of
// "found" until the first folder search answers. Without it the first listed
// read, which lands before the walk finishes, would re-merge against an empty
// found set and drop every row a caller handed in that is not pinned.
func unpinnedProjects(projects []project.Project) []project.Project {
	var out []project.Project
	for _, p := range projects {
		if !p.Pinned {
			out = append(out, p)
		}
	}
	return out
}

// scanExtraPaths lists the rows a scan pass must cover beyond what its own walk
// finds: the pinned start-up row, the listed projects and the folders a new
// plan was started in. No search folder produces any of them, so leaving them
// out would give the switcher rows with no session counts and no
// click-to-focus.
//
// It reads the ROWS rather than the listed set: a listed entry whose folder is
// gone, or one the start-up row stands in for, has no row, and resolving a
// rendezvous for it would open a watcher on every pass that applyRendezvous
// then has to close.
func scanExtraPaths(rows []project.Project, listed []ListedProject, started []string) []string {
	isListed := make(map[string]bool, len(listed))
	for _, e := range listed {
		if !e.Gone {
			isListed[e.Path] = true
		}
	}
	isStarted := make(map[string]bool, len(started))
	for _, path := range started {
		isStarted[path] = true
	}
	var paths []string
	for _, p := range rows {
		if p.Pinned || isListed[p.Path] || isStarted[p.Path] {
			paths = append(paths, p.Path)
		}
	}
	return paths
}

// sameListedPaths reports whether two listed sets would give the scan the same
// folders to cover: the same paths that still exist, in the same order.
func sameListedPaths(a, b []ListedProject) bool {
	live := func(in []ListedProject) []string {
		var out []string
		for _, e := range in {
			if !e.Gone {
				out = append(out, e.Path)
			}
		}
		return out
	}
	x, y := live(a), live(b)
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// mergedProjects is mergeProjects over what the model holds now.
func (m Model) mergedProjects() []project.Project {
	return mergeProjects(mergeInput{
		held:               m.projects,
		launchMainCheckout: m.deps.LaunchMainCheckout,
		listed:             m.listed,
		found:              m.found,
		started:            m.startedProjects,
	})
}

// applyListedProjects folds one read of the project list into the Model and
// rebuilds an open switcher only when its rows changed, for the reason
// applyProjectScan gives: rebuilding resets the cursor and any filter under
// the hands of whoever has it open.
func (m Model) applyListedProjects(msg listedProjectsMsg) (tea.Model, tea.Cmd) {
	before := m.projects
	nm, cmds := m.applyListedResult(msg)
	if !sameProjectPaths(before, nm.projects) {
		nm.refreshSwitcher()
	}
	return nm, tea.Batch(cmds...)
}

// applyListedResult is applyListedProjects' state transition.
//
// The active project is re-found by path after the merge, through the same
// settleActiveProject the scan uses, so a read that reorders the list cannot
// repoint the dashboard at whichever project now sits at the old index.
//
// A read that changed which folders are listed asks for one scan pass. scanCmd
// captures the listed paths when it is created, and at start-up it is created
// beside the first listed read, before any listed path is known; without this
// follow-up a listed row would show no session counts and take no click until
// the scan ticker next fires, a minute later. It is skipped when the listed
// folders did not change, so the steady state costs no extra walk.
func (m Model) applyListedResult(msg listedProjectsMsg) (Model, []tea.Cmd) {
	next, notice := foldListedRead(m.listed, msg.entries, msg.path, msg.err)
	m.notifyListedError(notice)
	if msg.err != nil {
		return m, nil
	}
	changed := !sameListedPaths(m.listed, next)
	m.listed = next
	was := m.activeProjectPath()
	m.projects = m.mergedProjects()
	m, cmds := m.settleActiveProject(was)
	if changed {
		if scan := m.scanCmd(); scan != nil {
			cmds = append(cmds, scan)
		}
	}
	return m, cmds
}

// notifyListedError raises the unreadable-project-list toast once per distinct
// text, and re-arms on the next good read (an empty notice), copying
// notifyScanError: the list is read again on every scan tick, so an unguarded
// call would toast every minute for a file that stays broken.
func (m *Model) notifyListedError(notice string) {
	if notice == "" {
		m.notifiedListedErr = ""
		return
	}
	if notice == m.notifiedListedErr {
		return
	}
	m.notifiedListedErr = notice
	m.notifyBackground(notifyError, bgListedError, notice)
}

// raiseStartupNotice shows the caller's start-up notice (Deps.StartupNotice)
// as one toast. New is its only caller, which is the whole of the once-per-run
// rule: nothing on a tick, a scan or a listed read comes back here, so unlike
// notifyListedError there is no "already raised" field to keep.
//
// It goes through notifyBackground like every other thing the dashboard says
// unasked, and is flushed at once. Left pending, it would land at the end of
// the first Update together with whatever that message raised, and two
// background events collapse to counts ("1 scan error, 1 ..."): the notice's
// own words would be lost, and so would the other event's, for good, since its
// emitter has already marked it as shown. Flushed here it is always alone
// (nothing is pending and no toast is live in a Model under construction), and
// a background event that arrives while it is up waits its turn.
//
// Because it is always alone, its category is never turned into a count.
// bgScanError is passed because the argument is required and that is the
// nearest subject: the notice is about the folder search's settings. It is a
// warning, not an error: nothing failed, there is something to do.
func (m *Model) raiseStartupNotice(text string) {
	if text == "" {
		return
	}
	m.notifyBackground(notifyWarn, bgScanError, text)
	m.flushBackground()
}
