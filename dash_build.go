package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dphaener/afk/internal/buildview"
	"github.com/dphaener/afk/internal/daemonstate"
	"github.com/dphaener/afk/internal/inbox"
	"github.com/dphaener/afk/internal/ledger"
	"github.com/dphaener/afk/internal/projectconfig"
	"github.com/dphaener/afk/internal/ui"
	"github.com/dphaener/afk/internal/usagestate"
	"github.com/dphaener/afk/internal/web"
)

// This file is the real version of the two build seams `afk dash` hands the
// dashboard (ui.Deps.LoadBuild and ui.Deps.AnswerInbox). It lives here and not
// in internal/ui because the pieces it joins are in this package: the main
// checkout lookup (maincheckout.go) and the plan and ledger loader
// (buildstate.go), which `afk tasks`, the daemon and the web page all share.
//
// It only reads files, plus one bounded `git rev-parse` to find the main
// checkout. It never dials a session's host or the network, so a wedged
// session cannot hold the dashboard's refresh. The dashboard calls it from
// inside a command, off its Update goroutine, on its two-second tick.
//
// It shares nothing in memory with the daemon, for the reason daemon_web.go
// gives: files are the one way every afk process talks to another, so the
// dashboard shows what the build loop wrote and the loop sees an answer on
// its next pass.

// dashBuildParts are the lookups dashLoadBuild does outside the project's own
// files, injected so the loader can be tested against temp folders without the
// machine's usage file, the real clock or the local time zone.
type dashBuildParts struct {
	// gitCommonDir is the git common dir as seen from dir, "" when dir is not
	// in a repository.
	gitCommonDir func(dir string) string
	// usage is the machine's last usage reading, false when there is none.
	usage func() (usagestate.File, bool)
	now   func() time.Time
	// loc is the zone a usage wake time is shown in.
	loc *time.Location
}

func realDashBuildParts() dashBuildParts {
	return dashBuildParts{
		gitCommonDir: resolveGitCommonDirIn,
		usage:        func() (usagestate.File, bool) { return usagestate.Read(usagestate.DefaultDeps()) },
		now:          time.Now,
		loc:          time.Local,
	}
}

// dashLoadBuild is ui.Deps.LoadBuild for the real dashboard.
func dashLoadBuild(repo string, sessions []ui.BuildSession) buildview.Snapshot {
	return loadDashBuild(realDashBuildParts(), repo, sessions)
}

// loadDashBuild reads everything the dashboard shows about a project's build.
//
// repo is the folder the dashboard is showing, which can be a session's linked
// worktree. The ledger and the inbox live in the main checkout (docs/PLAN.md
// section 4), and a worktree has its own git-ignored .afk/, so reading repo as
// it is would show an empty build from inside a session. The main checkout is
// worked out first and everything is read from there.
//
// The inbox and the usage reading are filled in whatever happens to the plan
// and the ledger: a damaged ledger is exactly when the owner most needs to
// read what the build asked them.
func loadDashBuild(p dashBuildParts, repo string, sessions []ui.BuildSession) buildview.Snapshot {
	root := mainCheckout(repo, p.gitCommonDir(repo))
	snap := buildview.Snapshot{Root: root}
	snap.Usage, snap.UsageKnown = p.usage()

	items, unreadable, err := inbox.Store{Dir: inbox.Dir(root)}.List()
	snap.Items = items
	snap.Unreadable = len(unreadable)
	if err != nil {
		// The folder itself could not be listed (a permission problem, a file
		// in its place). The snapshot has no field for that, and Problem is
		// the plan's and the ledger's: setting it would hide the task rows
		// behind an inbox fault. Counting the folder as one unreadable file
		// keeps the fault on screen, where an empty inbox would say all is
		// well.
		snap.Unreadable = 1
	}

	snap.HasBuild = dashHasBuild(root)
	if !snap.HasBuild {
		return snap
	}

	bs, loadErr := loadBuildState(root)
	stopTask, stopReason, stopped := daemonstate.ReadStop(root)
	daemonState, stateKnown := daemonstate.ReadState(root)
	// The same summary the web page shows (daemon_web.go and
	// internal/web/handlers.go build it from the same reads), so the count,
	// the current task and the last ship cannot differ between the two.
	sum := web.Summarize(web.SummaryInput{
		Name:       filepath.Base(root),
		Tasks:      bs.Tasks,
		Ledger:     bs.Ledger,
		LoadErr:    loadErr,
		State:      daemonState,
		StateKnown: stateKnown,
		Paused:     daemonstate.Paused(root),
		Stopped:    stopped,
		StopTask:   stopTask,
		StopReason: stopReason,
		Sessions:   dashSessionViews(sessions),
		Items:      items,
		Now:        p.now(),
	})

	// Paused and stopped come from their own marker files, which are read
	// whether or not the plan is; they stay true beside a problem.
	snap.Paused = sum.Paused
	if sum.Stopped != nil {
		snap.Stopped = true
		snap.StopTask = sum.Stopped.Task
		snap.StopReason = sum.Stopped.Reason
	}
	snap.LastShip = sum.LastShip.Text

	if loadErr != nil {
		// No rows, no count and no current task: guessing them from half the
		// data would mislead, which is the web page's rule too.
		snap.Problem = dashSentence(sum.Problem)
		return snap
	}

	snap.Done, snap.Total = sum.Done, sum.Total
	if sum.Current != nil {
		snap.Current = sum.Current.Task
		// A ship word is stored with underscores ("pr_open"); the task rows
		// show it with spaces, so the summary line does too.
		snap.CurrentPhase = strings.ReplaceAll(sum.Current.Phase, "_", " ")
	}
	snap.Rows = buildview.Rows(buildview.RowsInput{
		Tasks:    bs.Tasks,
		Ledger:   bs.Ledger,
		Sessions: dashSessionPhases(sessions),
		Items:    items,
		Loc:      p.loc,
	})
	snap.PausedSessions = buildview.PausedSessions(bs.Ledger, p.loc)
	return snap
}

// dashHasBuild reports whether the project has a build to show: a ledger, or
// the master plan file afk.toml names (docs/PLAN.md when it names none).
//
// A project with neither is simply not built by afk, and that is not a
// problem to report. Two cases count as a build although no plan was found,
// so that the reason reaches the screen and the Build tab does not just
// vanish: an afk.toml that cannot be read (the plan's path is then unknown,
// and the file needs fixing), and a plan file that is there but cannot be
// looked at.
func dashHasBuild(root string) bool {
	if ledger.Exists(root) {
		return true
	}
	cfg, err := projectconfig.Load(root)
	if err != nil {
		return true
	}
	_, err = os.Stat(cfg.PlanPath(root))
	return !errors.Is(err, fs.ErrNotExist)
}

// dashSessionViews hands the dashboard's rows to the summary in the shape the
// web page uses for discovery's rows (webSessionViews in daemon_web.go). Pure.
func dashSessionViews(sessions []ui.BuildSession) []web.SessionView {
	views := make([]web.SessionView, 0, len(sessions))
	for _, s := range sessions {
		views = append(views, web.SessionView{
			Name:          s.Name,
			Phase:         s.Phase,
			Stale:         s.Stale,
			AwaitingInput: s.AwaitingInput,
			HostRunning:   s.HostRunning,
		})
	}
	return views
}

// dashSessionPhases maps each session to its phase for the task rows, with ""
// for a phase that cannot be trusted. A stale status.json still holds the
// phase the session was in when it last wrote, and showing that beside a
// running task would state as current something nobody has confirmed. Pure.
func dashSessionPhases(sessions []ui.BuildSession) map[string]string {
	phases := make(map[string]string, len(sessions))
	for _, s := range sessions {
		if s.Stale {
			phases[s.Name] = ""
			continue
		}
		phases[s.Name] = s.Phase
	}
	return phases
}

// dashSentence turns a load error into a sentence for the screen: a capital
// first letter and a full stop. The errors from loadBuildState are written to
// follow a command's name ("afk tasks: the master plan ... has a problem"), so
// they start in lower case and end without a stop. Pure.
func dashSentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	r, size := utf8.DecodeRuneInString(s)
	s = string(unicode.ToUpper(r)) + s[size:]
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}

// dashAnswerInbox is ui.Deps.AnswerInbox for the real dashboard: it records
// the owner's answer to one inbox item as answered from the dashboard. root is
// the main checkout, as the snapshot's Root gives it.
//
// The text is passed on as typed. Store.Answer turns it into the answer to
// keep with inbox.ResolveAnswer (a number that names an option becomes that
// option's text), and does so under the inbox lock against the item as it is
// on disk at that moment. Resolving here first would do it twice: on an item
// whose options are themselves numbers ("3", "1", "2"), typing 1 would pick
// "3" and the second pass would then turn that "3" into the third option.
//
// The first answer is the one kept: a second call returns the stored item
// with inbox.ErrAlreadyAnswered and changes nothing. Nothing else is written;
// the build loop un-parks the task on its next pass.
func dashAnswerInbox(root, id, text string) (inbox.Item, error) {
	return inbox.Store{Dir: inbox.Dir(root)}.Answer(id, text, inbox.ViaTUI)
}
